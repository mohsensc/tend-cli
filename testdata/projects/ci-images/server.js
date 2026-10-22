'use strict'

const http = require('node:http')

http
  .createServer((req, res) => {
    res.end('ok\n')
  })
  .listen(process.env.PORT || 8080)
