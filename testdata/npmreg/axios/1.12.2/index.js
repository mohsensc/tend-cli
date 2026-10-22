'use strict'

exports.get = async (url) => {
  const res = await fetch(url)
  return { status: res.status, data: JSON.parse(await res.text()) }
}
