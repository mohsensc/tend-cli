import io
import json
import unittest
from unittest import mock

from app import orders


def reply(body):
    return io.BytesIO(json.dumps(body).encode())


# The API sends both shapes of the total while clients move over.
ORDERS = {
    "data": [
        {"id": "o1", "status": "paid", "total_cents": 250, "total": {"amount": 250, "currency": "usd"}},
        {"id": "o2", "status": "void", "total_cents": 999, "total": {"amount": 999, "currency": "usd"}},
        {"id": "o3", "status": "paid", "total_cents": 100, "total": {"amount": 100, "currency": "usd"}},
    ]
}


class OrdersTest(unittest.TestCase):
    def test_total_skips_void_orders(self):
        with mock.patch("urllib.request.urlopen", return_value=reply(ORDERS)) as opened:
            self.assertEqual(orders.total(orders.fetch_orders()), 350)
        opened.assert_called_once_with("https://api.fixture.dev/v1/orders")

    def test_receipt_needs_an_id(self):
        with self.assertRaises(ValueError):
            orders.receipt("")


if __name__ == "__main__":
    unittest.main()
