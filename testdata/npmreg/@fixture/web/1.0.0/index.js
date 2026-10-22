'use strict'

function web() {
  const routes = {}
  const settings = {}
  return {
    configure(fn) { fn() },
    set(key, value) { settings[key] = value },
    get(path, handler) { routes[path] = handler },
    handle(path, query) { return routes[path](query) },
  }
}

web.createServer = (app) => ({ app, close() {} })

module.exports = web
