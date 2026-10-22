'use strict'

var web = require('@fixture/web')
var order = require('./order')

var app = web()

app.configure(function () {
  app.set('name', 'legacy-shop')
})

app.get('/health', function () {
  return 'ok'
})

app.get('/order', function (query) {
  return order.read(query).amount
})

module.exports = app

module.exports.server = function () {
  return web.createServer(app)
}
