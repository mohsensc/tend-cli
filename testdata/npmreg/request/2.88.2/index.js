'use strict'

exports.get = (url, cb) => {
  fetch(url).then((res) => res.text().then((body) => cb(null, { statusCode: res.status }, body)), cb)
}
