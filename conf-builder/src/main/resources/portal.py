# -*- coding:utf-8 -*-

# -- app config --
DEBUG = ${log.debug.python}

# -- db config --
DB_HOST = "${mysql.host}"
DB_PORT = ${mysql.port}
DB_USER = "${dbuser.portal.account}"
DB_PASS = "${dbuser.password}"
DB_NAME = "${dbname.portal}"

# -- cookie config --
SECRET_KEY = "${m.portal.secret.key}"
SESSION_COOKIE_NAME = "${m.portal.session.cookie}"
PERMANENT_SESSION_LIFETIME = 3600 * 24 * 30

UIC_ADDRESS = {
    'internal': '${url.fe}',
    'external': '${url.fe}',
    'login': '${url.fe}/auth/login?callback=${url.portal}/',
}

UIC_TOKEN = ''

MAINTAINERS = ['root']
CONTACT = 'user@127.0.0.1'

COMMUNITY = True

JSONCFG = {}
JSONCFG['shortcut'] = {}
JSONCFG['shortcut']['falconPortal']     = "${url.portal}"
JSONCFG['shortcut']['falconDashboard']  = "${url.dashboard}"
JSONCFG['shortcut']['grafanaDashboard'] = "${url.grafana}"
JSONCFG['shortcut']['falconAlarm']      = "${url.alarm}"
JSONCFG['shortcut']['falconUIC']        = "${url.fe}"

try:
    from frame.local_config import *
except Exception, e:
    print 'level=warning msg="%s"' % e

