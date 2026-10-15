import pathlib
import tempfile
import unittest

from prepare_release import prepare


class PrepareReleaseTest(unittest.TestCase):
    def setUp(self):
        scratch = pathlib.Path('.scratch')
        scratch.mkdir(exist_ok=True)
        self.directory = tempfile.TemporaryDirectory(dir=scratch)
        self.addCleanup(self.directory.cleanup)
        self.root = pathlib.Path(self.directory.name)
        (self.root / 'cmd/server').mkdir(parents=True)
        self.cli = self.root / 'cmd/server/cli.go'
        self.cli.write_text('package main\nconst version = "0.4.0"\n')
        self.readme = self.root / 'README.md'
        self.readme.write_text('Version 0.4.0, tag v0.4.0, archive 0.4.0-source.tar.gz\n')

    def test_updates_source_and_documented_release_urls(self):
        prepare('0.5.0', self.root)
        self.assertIn('const version = "0.5.0"', self.cli.read_text())
        self.assertEqual(self.readme.read_text(),
                         'Version 0.5.0, tag v0.5.0, archive 0.5.0-source.tar.gz\n')

    def test_invalid_or_old_version_changes_nothing(self):
        for version in ('v1.0.0', '1.0', '1.0.0-rc.1', '01.2.3',
                        '0.4.0', '0.3.9', '1.0.0\n', '$(exit 1)'):
            with self.subTest(version=version):
                before = (self.cli.read_text(), self.readme.read_text())
                with self.assertRaises(ValueError):
                    prepare(version, self.root)
                self.assertEqual(before, (self.cli.read_text(), self.readme.read_text()))


if __name__ == '__main__':
    unittest.main()
