#!/usr/bin/env python3
"""
Credits detection — finds the timestamp where end credits begin in a video.

Uses brightness analysis + Canny edge detection (no OCR).

Phase 1: Extract micro-thumbnails every 2s from last 20%, compute mean brightness.
         Find the last sustained dark region that extends to near the end.
Phase 2: Extract 1fps thumbnails around the transition for exact timing.
Phase 3: Edge detection on a few frames to confirm text presence (not just black).

Output: JSON to stdout: {"detected": true, "credits_start_sec": 3562.0}
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path

import numpy as np
from PIL import Image


# --- Thresholds ---
# A frame is "dark" if this fraction of pixels have brightness < 40
DARK_PIXEL_THRESH = 40
DARK_RATIO_GATE = 0.70
# Mid-tone check: credits have very few mid-tone pixels
MID_TONE_LO = 50
MID_TONE_HI = 200
MID_TONE_GATE = 0.30
# Minimum sustained dark seconds to count as credits
MIN_CREDITS_DURATION = 20
# Edge density threshold: fraction of pixels that are edges (Canny)
# Credits text produces edges; a plain black screen does not.
EDGE_DENSITY_THRESH = 0.005


def vlog(msg, verbose=False):
    if verbose:
        print(msg, file=sys.stderr, flush=True)


def format_time(seconds):
    m, s = divmod(int(seconds), 60)
    h, m = divmod(m, 60)
    if h:
        return f"{h}:{m:02d}:{s:02d}"
    return f"{m}:{s:02d}"


def extract_thumbs(video_url, start_sec, count, fps, out_dir, prefix="thumb", width=160):
    """Extract small thumbnails — fast to decode and process."""
    pattern = os.path.join(out_dir, f"{prefix}_%05d.png")
    cmd = [
        "ffmpeg", "-y", "-v", "quiet",
        "-ss", str(start_sec),
        "-i", video_url,
        "-vf", f"fps={fps},scale={width}:-1",
        "-frames:v", str(count),
        pattern,
    ]
    subprocess.run(cmd, check=True, timeout=180)
    frames = sorted(Path(out_dir).glob(f"{prefix}_*.png"))
    return [str(f) for f in frames]


def frame_brightness(img_path):
    """Return mean brightness (0-255) of a grayscale frame."""
    img = Image.open(img_path).convert("L")
    pixels = np.array(img, dtype=np.uint8)
    return float(np.mean(pixels))


def is_dark(img_path):
    """Check if a frame is dark enough to be credits (fast, <1ms)."""
    return frame_brightness(img_path) < 35


def edge_density(img_path):
    """Compute Canny edge density — high for text on dark bg, low for plain black."""
    img = Image.open(img_path).convert("L")
    pixels = np.array(img, dtype=np.uint8)

    # Simple Sobel-based edge detection (no OpenCV dependency)
    # Horizontal and vertical gradients
    gx = np.abs(pixels[:, 2:].astype(np.int16) - pixels[:, :-2].astype(np.int16))
    gy = np.abs(pixels[2:, :].astype(np.int16) - pixels[:-2, :].astype(np.int16))
    # Crop to same size
    gx = gx[1:-1, :]
    gy = gy[:, 1:-1]
    edges = np.sqrt(gx.astype(np.float32) ** 2 + gy.astype(np.float32) ** 2)
    # Threshold: pixel is an edge if gradient magnitude > 30
    edge_pixels = float(np.mean(edges > 30))
    return edge_pixels


def detect_credits(video_url, duration, verbose=False):
    """
    Three-phase detection:
      Phase 1: Brightness scan (every 2s) — find sustained dark region at end.
      Phase 2: Fine scan (1fps) — exact transition timestamp.
      Phase 3: Edge confirmation — verify text presence (not just black screen).
    """
    tmp_dir = tempfile.mkdtemp(prefix="credits_")

    try:
        # Phase 1: coarse brightness scan of last 8 minutes (or 15%, whichever is less)
        max_scan_secs = 480  # 8 minutes max
        scan_start = max(0, duration - min(duration * 0.15, max_scan_secs))
        scan_duration = duration - scan_start
        coarse_interval = 10
        coarse_count = min(int(scan_duration / coarse_interval), 50)

        vlog(f"Phase 1: brightness scan from {format_time(scan_start)} "
             f"({coarse_count} frames, {coarse_interval}s apart)...", verbose)

        thumbs = extract_thumbs(video_url, scan_start, coarse_count,
                                fps=f"1/{coarse_interval}", out_dir=tmp_dir,
                                prefix="coarse")
        vlog(f"  Extracted {len(thumbs)} thumbnails", verbose)

        # Classify each frame as dark (D) or scene (S)
        dark_flags = []
        for f in thumbs:
            dark_flags.append(is_dark(f))

        vlog(f"  {sum(dark_flags)}/{len(dark_flags)} dark frames", verbose)

        timeline = "".join("D" if d else "S" for d in dark_flags)
        # Print timeline in rows of 60
        if verbose:
            for row_start in range(0, len(timeline), 60):
                chunk = timeline[row_start:row_start + 60]
                ts = format_time(scan_start + row_start * coarse_interval)
                vlog(f"  {ts} {chunk}", verbose)

        # Find the LAST sustained dark run that extends to near the end.
        # "Near the end" = within 60s of video end (allows for post-credits bumper).
        # Walk backward from end to find where the dark region starts.
        total_frames = len(dark_flags)
        end_tolerance = int(60 / coarse_interval)  # frames within 60s of end

        # Check that frames near the end are dark
        end_check_start = max(0, total_frames - end_tolerance)
        end_dark_count = sum(1 for d in dark_flags[end_check_start:] if d)
        end_dark_ratio = end_dark_count / max(1, total_frames - end_check_start)

        if end_dark_ratio < 0.5:
            vlog(f"  End region not dark enough ({end_dark_ratio:.0%}), no credits", verbose)
            return {"detected": False}

        # Walk backward from end to find where sustained darkness begins.
        # Allow up to 2 non-dark frames as gaps (scene flash within credits).
        max_gap = 2
        gap_count = 0
        dark_run_start = total_frames - 1

        for i in range(total_frames - 1, -1, -1):
            if dark_flags[i]:
                dark_run_start = i
                gap_count = 0
            else:
                gap_count += 1
                if gap_count > max_gap:
                    break

        dark_run_duration = (total_frames - dark_run_start) * coarse_interval
        transition_estimate = scan_start + (dark_run_start * coarse_interval)

        vlog(f"  Dark run: {format_time(transition_estimate)} → end "
             f"({dark_run_duration}s)", verbose)

        if dark_run_duration < MIN_CREDITS_DURATION:
            vlog(f"  Dark run too short ({dark_run_duration}s < "
                 f"{MIN_CREDITS_DURATION}s), no credits", verbose)
            return {"detected": False}

        # Phase 2: fine 1fps scan around the transition
        fine_start = max(0, transition_estimate - 15)
        fine_count = 30  # 30s window around transition

        vlog(f"Phase 2: fine scan {format_time(fine_start)} "
             f"({fine_count} frames @ 1fps)...", verbose)

        fine_thumbs = extract_thumbs(video_url, fine_start, fine_count,
                                     fps=1, out_dir=tmp_dir, prefix="fine")
        vlog(f"  Extracted {len(fine_thumbs)} thumbnails", verbose)

        fine_dark = [is_dark(f) for f in fine_thumbs]
        fine_tl = "".join("D" if d else "S" for d in fine_dark)
        vlog(f"  {format_time(fine_start)} {fine_tl}", verbose)

        # Find the transition: last S→D that leads into a sustained dark run
        # Walk backward from end of fine scan to find where dark started
        fine_transition = 0
        gap = 0
        for i in range(len(fine_dark) - 1, -1, -1):
            if fine_dark[i]:
                fine_transition = i
                gap = 0
            else:
                gap += 1
                if gap > 1:
                    break

        transition_sec = fine_start + fine_transition

        vlog(f"  Transition at {format_time(transition_sec)}", verbose)

        # Phase 3: edge confirmation — verify credits have text, not just black
        vlog(f"Phase 3: edge confirmation...", verbose)

        # Sample 3 frames from middle of the dark region
        dark_middle = transition_sec + dark_run_duration / 2
        sample_times = [dark_middle - 10, dark_middle, dark_middle + 10]
        sample_times = [max(transition_sec + 5, min(t, duration - 5)) for t in sample_times]

        has_text = False
        for st in sample_times:
            sample_frames = extract_thumbs(video_url, st, 1, fps=1,
                                           out_dir=tmp_dir, prefix=f"edge_{int(st)}",
                                           width=960)  # higher res for edge detection
            if sample_frames:
                density = edge_density(sample_frames[0])
                vlog(f"  Edge density at {format_time(st)}: {density:.4f} "
                     f"(thresh={EDGE_DENSITY_THRESH})", verbose)
                if density >= EDGE_DENSITY_THRESH:
                    has_text = True
                    break

        if not has_text:
            vlog("  No text edges detected — plain black ending, not credits", verbose)
            return {"detected": False}

        vlog(f"  Confirmed credits at {format_time(transition_sec)}", verbose)

        return {
            "detected": True,
            "credits_start_sec": round(transition_sec, 1),
        }

    finally:
        import shutil
        shutil.rmtree(tmp_dir, ignore_errors=True)


def main():
    parser = argparse.ArgumentParser(description="Detect credits start in video")
    parser.add_argument("--url", required=True, help="Video URL or path")
    parser.add_argument("--duration", required=True, type=float, help="Video duration in seconds")
    parser.add_argument("--verbose", action="store_true", help="Print progress to stderr")
    args = parser.parse_args()

    result = detect_credits(args.url, args.duration, args.verbose)
    print(json.dumps(result))


if __name__ == "__main__":
    main()
