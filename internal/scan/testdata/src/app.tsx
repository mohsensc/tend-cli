import Stripe from 'stripe';
import * as aws from "aws-sdk";
import { S3Client, PutObjectCommand as Put } from '@aws-sdk/client-s3';
import type { Charge } from 'stripe/types';
import def, { helper } from "lodash/fp";
import './polyfill';
import 'reflect-metadata';
import local from './local';
const axios = require('axios');
const { createClient, Pool: PgPool } = require("pg");
export { thing } from 'some-lib';
import { Injectable, Input } from '@nestjs/common';

export async function main(key: string, id: string) {
  const stripe = new Stripe(key, { apiVersion: '2024-06-20' });
  const charge = await stripe.charges.create({ amount: 2000, currency: 'usd' });
  console.log(charge.id); // stripe.charges.retrieve('x') in a comment
  const s3 = new S3Client({});
  await s3.send(new Put({ Bucket: 'b', Key: 'k' }));
  await stripe?.customers?.del('cus_1');
  stripe['paymentIntents'].create({});
  aws.config.update({ region: 'us-east-1' });
  const res = await axios.post('https://api.example.com/v1/orders', { total_cents: 5 });
  const r2 = await fetch(`${BASE}/v1/orders/${id}`, {
    method: 'DELETE',
  });
  const pool = new PgPool();
  pool.query('select 1');
  const db = createClient();
  db.connect();
  const { default: chalk } = await import('chalk');
  const mod = await import("dayjs");
  mod.extend({});
  helper(def);
  const msg = "stripe.charges.create is only a string";
  const re = /'stripe'/g;
  return <a href="https://docs.example.com/x">Don't {stripe.balance.retrieve()}</a>;
}

@Injectable()
class Billing {
  @Input
  name: string;
  constructor(key: string) {
    this.client = new Stripe(key);
  }
  refund() {
    return this.client.refunds.create({});
  }
}
