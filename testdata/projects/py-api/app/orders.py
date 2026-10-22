import json
import urllib.request

BASE = "https://api.fixture.dev"


def fetch_orders():
    """Every order on the account, newest first."""
    with urllib.request.urlopen(BASE + "/v1/orders") as res:
        return json.load(res)["data"]


def total(orders):
    cents = 0
    for order in orders:
        if order.get("status") != "void":
            cents += order["total_cents"]
    return cents


def receipt(order_id):
    """The old receipt format, which the accounting export still reads."""
    if not order_id:
        raise ValueError("receipt needs an order id")
    with urllib.request.urlopen(BASE + "/v1/orders/%s/legacy" % order_id) as res:
        return json.load(res)
