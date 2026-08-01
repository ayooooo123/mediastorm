import unittest

from download_subtitle import ass_to_vtt, srt_to_vtt


class SrtToVttTests(unittest.TestCase):
    def test_strips_ass_position_override_blocks(self):
        source = """1
00:00:33,860 --> 00:00:35,850
{\\an4\\pos(273,402)}[birds chirping]
"""

        converted = srt_to_vtt(source)

        self.assertEqual(
            converted,
            "WEBVTT\n\n00:00:33.860 --> 00:00:35.850\n[birds chirping]\n",
        )
        self.assertNotIn("\\pos", converted)

    def test_strips_inline_ass_overrides_without_removing_text(self):
        source = """1
00:00:01,000 --> 00:00:02,000
{\\i1}Hello{\\i0} world
"""

        converted = srt_to_vtt(source)

        self.assertIn("\nHello world\n", converted)


class AssToVttTests(unittest.TestCase):
    def test_strips_ass_position_override_blocks(self):
        source = """[Script Info]
[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:33.86,0:00:35.85,Default,,0,0,0,,{\\an4\\pos(273,402)}[birds chirping]
"""

        converted = ass_to_vtt(source)

        self.assertEqual(
            converted,
            "WEBVTT\n\n00:00:33.860 --> 00:00:35.850\n[birds chirping]\n",
        )
        self.assertNotIn("\\pos", converted)


if __name__ == "__main__":
    unittest.main()
