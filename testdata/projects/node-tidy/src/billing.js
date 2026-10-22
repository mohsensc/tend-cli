'use strict'

const pay = require('@fixture/pay')

function cents(dollars) {
  return Math.round(dollars * 100)
}

async function charge(dollars, customer) {
  const amount = cents(dollars)
  if (amount <= 0) throw new RangeError('nothing to charge')
  return pay.charges.create({ amount, currency: 'usd', customer })
}

async function lookup(id) {
  if (typeof id !== 'string' || id === '') {
    throw new TypeError('lookup needs an id')
  }

  return pay.charges.retrieve(id)
}

module.exports = { charge, lookup }
