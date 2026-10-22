```js
'use strict'

const test = require('node:test')
const assert = require('node:assert')
const app = require('../../src/app')
const client = require('../../src/client')

test('health answers ok', () => {
  assert.strictEqual(app.handle('/health'), 'ok')
})

test('an order reads its amount from the query', () => {
  assert.strictEqual(app.handle('/order', '42'), 42)
})

test('the server starts and stops', () => {
  const server = app.server()
  assert.strictEqual(typeof server.close, 'function')
  server.close()
})

test('a price is read in cents from the price service', (t, done) => {
  globalThis.fetch = async () => ({ status: 200, text: async () => '{"cents":1250}' })
  client.price('sku-1', (err, cents) => {
    assert.ifError(err)
    assert.strictEqual(cents, 1250)
    done()
  })
})
```
