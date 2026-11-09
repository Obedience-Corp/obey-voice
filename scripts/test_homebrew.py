"""Verify that the tap renderer only consumes complete, checksum-verified releases."""

from render_homebrew import render
from test_release_artifacts import PayloadFixture


class HomebrewRendererTests(PayloadFixture):
    def test_complete_release_uses_verified_hashes_and_canonical_urls(self):
        linux = self.payload("linux-amd64")
        macos = self.payload("macos-arm64")
        output = self.root / "Formula/obey-voice.rb"
        render(self.output, self.tag, output)
        formula = output.read_text()
        self.assertNotIn('  version "', formula)
        for archive in (linux, macos):
            self.assertIn(f"https://github.com/Obedience-Corp/obey-voice/releases/download/{self.tag}/{archive.name}", formula)
            checksum = archive.with_name(archive.name + ".sha256").read_text().split()[0]
            self.assertIn(checksum, formula)
        self.assertNotIn("@@", formula)

    def test_prerelease_keeps_exact_version_override(self):
        self.tag = "v1.2.3-rc.1"
        self.payload("linux-amd64")
        self.payload("macos-arm64")
        output = self.root / "obey-voice.rb"
        render(self.output, self.tag, output)
        self.assertIn('  version "1.2.3-rc.1"', output.read_text())

    def test_incomplete_release_does_not_replace_previous_formula(self):
        self.payload("linux-amd64")
        output = self.root / "obey-voice.rb"
        output.write_text("previous formula\n")
        with self.assertRaises(FileNotFoundError):
            render(self.output, self.tag, output)
        self.assertEqual(output.read_text(), "previous formula\n")

    def test_corrupted_payload_never_generates_formula(self):
        archive = self.payload("linux-amd64")
        self.payload("macos-arm64")
        archive.write_bytes(archive.read_bytes() + b"corrupt")
        output = self.root / "obey-voice.rb"
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            render(self.output, self.tag, output)
        self.assertFalse(output.exists())
