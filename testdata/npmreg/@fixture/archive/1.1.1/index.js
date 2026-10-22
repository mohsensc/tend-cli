'use strict'

exports.pack = (s) => String(s).replace(/\.\.\//g, '')
