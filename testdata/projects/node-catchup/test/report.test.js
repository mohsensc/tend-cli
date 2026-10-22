'use strict'

const test = require('node:test')
const assert = require('node:assert')
const report = require('../src/report')

test('reports an amount in dollars', () => {
  assert.strictEqual(report('5'), '$5.00')
})
