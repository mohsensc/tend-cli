'use strict'

const base = process.env.FIXTURE_PAY_URL || 'https://pay.fixture.dev'

async function call(method, path, body) {
  const res = await fetch(base + path, {
    method,
    headers: { 'content-type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!res.ok) {
    throw new Error(`@fixture/pay: ${method} ${path}: ${res.status}`)
  }
  return res.json()
}

exports.paymentIntents = {
  create: (params) => call('POST', '/v1/payment_intents', params),
  retrieve: (id) => call('GET', '/v1/payment_intents/' + encodeURIComponent(id)),
}
