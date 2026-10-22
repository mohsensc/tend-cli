'use strict'

function web() {
  const routes = {}
  const settings = {}
  const app = {
    set(key, value) { settings[key] = value },
    get(path, handler) { routes[path] = handler },
    handle(path, query) { return routes[path](query) },
    createServer() { return { app, close() {} } },
  }
  return app
}

module.exports = web
