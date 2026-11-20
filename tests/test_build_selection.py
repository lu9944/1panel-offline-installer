import importlib.util
import json
import shutil
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location("builder", ROOT / "scripts" / "build-offline-package.py")
builder = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(builder)


def write(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


class VersionSelectionTest(unittest.TestCase):
    def test_mysql_defaults_to_highest_8_0(self):
        versions = ["5.7.44", "8.0.45", "8.0.46", "8.4.13"]
        self.assertEqual(builder.pick_app_version("mysql", versions, ""), "8.0.46")

    def test_mysql_override_is_validated(self):
        with self.assertRaises(SystemExit):
            builder.pick_app_version("mysql", ["8.0.46"], "8.0.99")

    def test_openresty_uses_numeric_version_order(self):
        versions = ["1.21.4.3-3-3-focal", "1.27.1.2-5-1-focal", "1.31.8.1-2-2-noble"]
        self.assertEqual(builder.pick_app_version("openresty", versions, ""), "1.31.8.1-2-2-noble")

    def test_redis_ignores_non_semver_directories(self):
        versions = ["6.2.24", "README", "8.10.3", "8.10.3-rc1"]
        self.assertEqual(builder.pick_app_version("redis", versions, ""), "8.10.3")


class MetadataTest(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="1panel-builder-test-"))
        app = self.tmp / "appstore" / "apps" / "mysql"
        self.app = app
        write(app / "data.yml", """
additionalProperties:
  key: mysql
  name: MySQL
  type: runtime
  tags: [Database]
  architectures: [amd64, arm64]
  shortDescZh: 数据库
  shortDescEn: Database
  recommend: 5
  limit: 0
  batchInstallSupport: true
  website: https://mysql.com
  github: https://github.com/mysql
  document: https://dev.mysql.com
""")
        (app / "logo.png").write_bytes(b"\x89PNG\r\n\x1a\n")
        write(app / "README.md", "# MySQL")
        for version in ("8.0.46", "8.4.13"):
            write(app / version / "data.yml", """
additionalProperties:
  formFields:
    - default: mysql
      envKey: PANEL_DB_ROOT_PASSWORD
      type: password
      required: true
    - default: 3306
      envKey: PANEL_APP_PORT_HTTP
      type: number
      required: true
""")
            write(app / version / "docker-compose.yml", f"services:\n  mysql:\n    image: mysql:{version}\n")
        write(app / "8.0.46" / "build" / "tmp" / "huge.tar.gz", "x" * 100)

    def tearDown(self):
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_metadata_and_copy_only_selected_version(self):
        meta, copied = builder.build_apps_metadata(self.tmp / "appstore", {"mysql": "8.0.46"}, "2026-01-01T00:00:00Z", 123)
        self.assertEqual(meta["apps"][0]["version"], "8.0.46")
        self.assertEqual(meta["apps"][0]["image"], "mysql:8.0.46")
        self.assertEqual(meta["apps"][0]["tags"], ["Database"])
        self.assertEqual(meta["apps"][0]["params"]["formFields"][0]["envKey"], "PANEL_DB_ROOT_PASSWORD")

        dest = self.tmp / "out"
        builder.copy_app_dirs(copied, dest)
        self.assertTrue((dest / "mysql" / "8.0.46" / "docker-compose.yml").is_file())
        self.assertFalse((dest / "mysql" / "8.4.13").exists())
        self.assertFalse((dest / "mysql" / "8.0.46" / "build" / "tmp").exists())


if __name__ == "__main__":
    unittest.main()
