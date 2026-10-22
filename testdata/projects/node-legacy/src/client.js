'use strict'

var request = require('request')

exports.price = function (sku, cb) {
  request.get('https://prices.example.test/v1/' + sku, function (err, res, body) {
    if (err) return cb(err)
    cb(null, JSON.parse(body).cents)
  })
}
