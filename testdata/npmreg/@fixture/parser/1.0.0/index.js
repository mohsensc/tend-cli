'use strict'

const { pack } = require('@fixture/archive')

exports.parse = (s) => ({ amount: Number(s), raw: pack(s) })
