'use strict'

const { pack } = require('@fixture/archive')

exports.parse = (s) => {
  if (typeof s !== 'string') throw new TypeError('parse needs a string')
  return { amount: Number(s), raw: pack(s) }
}
