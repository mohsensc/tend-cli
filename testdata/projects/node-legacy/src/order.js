'use strict'

var parser = require('@fixture/parser')

exports.read = function (query) {
  return parser.parse(query)
}
