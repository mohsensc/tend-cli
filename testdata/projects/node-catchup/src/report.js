'use strict'

const { parse } = require('@fixture/parser')
const { money } = require('@fixture/format')

module.exports = (s) => money(parse(s).amount)
