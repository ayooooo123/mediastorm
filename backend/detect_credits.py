#!/usr/bin/env python3
"""
Credits detection — finds the timestamp where end credits begin in a video.

Uses FFmpeg keyframe extraction + EasyOCR text detection.
Phase 1: Extract keyframes from last 10% of video (skip_frame nokey).
Phase 2: Fine 1fps scan of 20s window around first detected credits.

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

# Lazy-load EasyOCR (slow import)
_reader = None
def get_reader():
    global _reader
    if _reader is None:
        import easyocr
        _reader = easyocr.Reader(['en'], gpu=False, verbose=False)
    return _reader


# --- Thresholds ---
DARK_RATIO_GATE = 0.70
MID_TONE_GATE = 0.30
TEXT_REGIONS_CREDITS = 3
TEXT_REGIONS_SPARSE = 1


def vlog(msg, verbose=False):
    if verbose:
        print(msg, file=sys.stderr, flush=True)


def extract_frames(video_url, start_sec, count, fps, out_dir, prefix="frame"):
    pattern = os.path.join(out_dir, f"{prefix}_{start_sec:.0f}_%04d.png")
    cmd = [
        "ffmpeg", "-y", "-v", "quiet",
        "-ss", str(start_sec),
        "-i", video_url,
        "-vf", f"fps={fps},scale=960:-1",
        "-frames:v", str(count),
        pattern,
    ]
    subprocess.run(cmd, check=True, timeout=120)
    frames = sorted(Path(out_dir).glob(f"{prefix}_{start_sec:.0f}_*.png"))
    return [str(f) for f in frames]


def analyze_frame_quick(img_path):
    img = Image.open(img_path).convert("L")
    pixels = np.array(img, dtype=np.float32)
    dark_ratio = float(np.mean(pixels < 40))
    mid_tones = float(np.mean((pixels >= 50) & (pixels <= 200)))
    return {
        "dark_ratio": round(dark_ratio, 3),
        "mid_tones": round(mid_tones, 3),
        "is_dark_enough": dark_ratio > DARK_RATIO_GATE and mid_tones < MID_TONE_GATE,
    }


def count_text_regions(img_path):
    reader = get_reader()
    try:
        result = reader.detect(img_path)
        h_boxes = len(result[0][0]) if result[0] else 0
        f_boxes = len(result[1][0]) if result[1] else 0
        return h_boxes + f_boxes
    except Exception:
        return 0


def is_credits_frame(img_path, quick=None):
    if quick is None:
        quick = analyze_frame_quick(img_path)

    if not quick["is_dark_enough"]:
        return False

    text_regions = count_text_regions(img_path)

    if quick["dark_ratio"] > 0.98:
        return text_regions >= TEXT_REGIONS_SPARSE
    return text_regions >= TEXT_REGIONS_CREDITS


def format_time(seconds):
    m, s = divmod(int(seconds), 60)
    h, m = divmod(m, 60)
    if h:
        return f"{h}:{m:02d}:{s:02d}"
    return f"{m}:{s:02d}"


def detect_credits(video_url, duration, verbose=False):
    """
    Two-phase forward scan:
      Phase 1: Keyframes from 90% onwards, find coarse credits position.
      Phase 2: Fine 1fps scan of 20s window around first coarse hit.
    Returns {"detected": bool, "credits_start_sec": float}
    """
    tmp_dir = tempfile.mkdtemp(prefix="credits_")

    try:
        # Phase 1a: fast coarse scan using keyframes only (I-frames, ~10s)
        scan_start = duration * 0.90
        scan_duration = duration - scan_start

        vlog(f"Phase 1a: keyframe scan from {format_time(scan_start)}...", verbose)
        pattern = os.path.join(tmp_dir, "coarse_%04d.png")
        cmd = [
            "ffmpeg", "-y", "-v", "quiet",
            "-skip_frame", "nokey",
            "-ss", str(scan_start),
            "-i", video_url,
            "-vf", "scale=960:-1",
            "-vsync", "vfr",
            "-frames:v", "100",
            pattern,
        ]
        subprocess.run(cmd, check=True, timeout=120)
        coarse_samples = sorted(str(p) for p in Path(tmp_dir).glob("coarse_*.png"))
        coarse_interval = scan_duration / max(len(coarse_samples), 1)

        vlog(f"  Got {len(coarse_samples)} keyframes (~{coarse_interval:.1f}s apart)", verbose)

        first_credits_sample = None
        consecutive_credits = 0

        for idx, f in enumerate(coarse_samples):
            quick = analyze_frame_quick(f)

            if not quick["is_dark_enough"]:
                consecutive_credits = 0
                continue

            if is_credits_frame(f, quick):
                consecutive_credits += 1
                if first_credits_sample is None:
                    first_credits_sample = idx
            elif quick["dark_ratio"] > 0.95 and quick["mid_tones"] < 0.05:
                pass  # black frame between credit cards
            else:
                if consecutive_credits < 2:
                    first_credits_sample = None
                consecutive_credits = 0

            if consecutive_credits >= 2 and first_credits_sample is not None:
                break

        # Phase 1b: if keyframes found nothing, fall back to 1/10fps regular extraction.
        # Keyframes land on scene boundaries and can miss scrolling credits text.
        if first_credits_sample is None:
            vlog("  Keyframes found nothing, falling back to 1/10fps scan...", verbose)
            fallback_interval = 10
            fallback_count = min(int(scan_duration / fallback_interval), 100)
            fb_pattern = os.path.join(tmp_dir, "fb_%04d.png")
            cmd = [
                "ffmpeg", "-y", "-v", "quiet",
                "-ss", str(scan_start),
                "-i", video_url,
                "-vf", f"fps=1/{fallback_interval},scale=960:-1",
                "-frames:v", str(fallback_count),
                fb_pattern,
            ]
            subprocess.run(cmd, check=True, timeout=180)
            coarse_samples = sorted(str(p) for p in Path(tmp_dir).glob("fb_*.png"))
            coarse_interval = fallback_interval

            vlog(f"  Got {len(coarse_samples)} frames ({fallback_interval}s apart)", verbose)

            for idx, f in enumerate(coarse_samples):
                quick = analyze_frame_quick(f)

                if not quick["is_dark_enough"]:
                    consecutive_credits = 0
                    continue

                if is_credits_frame(f, quick):
                    consecutive_credits += 1
                    if first_credits_sample is None:
                        first_credits_sample = idx
                elif quick["dark_ratio"] > 0.95 and quick["mid_tones"] < 0.05:
                    pass
                else:
                    if consecutive_credits < 2:
                        first_credits_sample = None
                    consecutive_credits = 0

                if consecutive_credits >= 2 and first_credits_sample is not None:
                    break

        if first_credits_sample is None:
            return {"detected": False}

        # Phase 2: fine scan
        # The coarse keyframe timestamp is approximate (index * average interval).
        # Keyframes are not evenly spaced, so the real timestamp could be off by
        # a full interval in either direction. Scan a wide window to be safe:
        # 20s before the estimated hit through 2*interval + 30s after.
        coarse_hit_sec = scan_start + (first_credits_sample * coarse_interval)
        fine_start = max(scan_start, coarse_hit_sec - 20)
        fine_count = int(20 + 2 * coarse_interval + 30)

        vlog(f"Phase 2: fine scan from {format_time(fine_start)}...", verbose)
        fine_frames = extract_frames(video_url, fine_start, count=fine_count,
                                     fps=1, out_dir=tmp_dir, prefix="fine")
        vlog(f"  Got {len(fine_frames)} frames", verbose)

        fine_states = []
        for f in fine_frames:
            quick = analyze_frame_quick(f)
            if not quick["is_dark_enough"]:
                fine_states.append("S")
                continue
            if is_credits_frame(f, quick):
                fine_states.append("C")
            elif quick["dark_ratio"] > 0.95 and quick["mid_tones"] < 0.05:
                fine_states.append(".")
            else:
                fine_states.append("S")

        vlog(f"  Fine timeline: {format_time(fine_start)} {''.join(fine_states)}", verbose)

        # Find exact transition
        first_credits_fine = None
        for i, s in enumerate(fine_states):
            if s == "C":
                first_credits_fine = i
                break

        if first_credits_fine is not None:
            transition_sec = fine_start + first_credits_fine
        else:
            transition_sec = max(scan_start, coarse_hit_sec - 5)

        return {
            "detected": True,
            "credits_start_sec": round(transition_sec, 1),
        }

    finally:
        # Clean up temp files
        import shutil
        shutil.rmtree(tmp_dir, ignore_errors=True)


def main():
    parser = argparse.ArgumentParser(description="Detect credits start in video")
    parser.add_argument("--url", required=True, help="Video URL or path")
    parser.add_argument("--duration", required=True, type=float, help="Video duration in seconds")
    parser.add_argument("--verbose", action="store_true", help="Print progress to stderr")
    args = parser.parse_args()

    # Pre-load OCR model
    vlog("Loading EasyOCR model...", args.verbose)
    get_reader()
    vlog("Model loaded", args.verbose)

    result = detect_credits(args.url, args.duration, args.verbose)
    print(json.dumps(result))


if __name__ == "__main__":
    main()
