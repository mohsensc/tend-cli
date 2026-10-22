'use strict'

exports.request = async (url) => {
  const res = await fetch(url)
  const text = await res.text()
  return { statusCode: res.status, body: { text: async () => text, json: async () => JSON.parse(text) } }
}
