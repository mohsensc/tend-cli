import unittest

from app import report


class ReportTest(unittest.TestCase):
    def test_total(self):
        self.assertEqual(report.total(), 350)


if __name__ == "__main__":
    unittest.main()
