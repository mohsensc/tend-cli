import os, json
import boto3
import boto3.session as bs
import google.cloud.storage
from stripe import Charge as StripeCharge, Customer
from openai import (
    OpenAI,
    AsyncOpenAI as Async,
)
from tenacity import retry
from .models import Order
from typing import *

API = "https://api.example.com"

client = OpenAI()
s3 = boto3.client("s3")


@app.route("/hooks")
@retry
def handler(event):
    """stripe.Charge.create in a docstring is not a site"""
    charge = StripeCharge.create(amount=2000, currency="usd")
    print(charge.id)
    Customer.retrieve("cus_1")
    resp = client.chat.completions.create(model="gpt-4o", messages=[])
    s3.upload_file("a.txt", "b", "a.txt")
    r = requests.post(f"{API}/v1/orders", json={"total_cents": 5})
    bucket = google.cloud.storage.Client().bucket("b")
    bs.Session()
    body = json.dumps({"a": 1})
    urllib.request.Request("/v1/refunds", method="PUT")
    return os.environ["X"]


class Svc:
    def __init__(self):
        self.ai = Async()

    async def go(self):
        await self.ai.models.list()
