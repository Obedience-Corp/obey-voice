#!/usr/bin/env python3
"""Private D-Bus watcher fixture. Run only inside dbus-run-session."""
import sys
import threading
import dbus
import dbus.service
from dbus.mainloop.glib import DBusGMainLoop
from gi.repository import GLib

DBusGMainLoop(set_as_default=True)
loop = GLib.MainLoop()
bus = dbus.SessionBus()
name = dbus.service.BusName('org.kde.StatusNotifierWatcher', bus)

class Watcher(dbus.service.Object):
    host = False

    @dbus.service.method('org.kde.StatusNotifierWatcher', in_signature='s')
    def RegisterStatusNotifierItem(self, _service):
        pass

    @dbus.service.method('org.freedesktop.DBus.Properties', in_signature='ss', out_signature='v')
    def Get(self, _interface, prop):
        if prop == 'IsStatusNotifierHostRegistered':
            return dbus.Boolean(self.host)
        raise dbus.DBusException('Unknown property')

watcher = Watcher(bus, '/StatusNotifierWatcher')

def set_host(value):
    watcher.host = value
    return False

def commands():
    for line in sys.stdin:
        GLib.idle_add(set_host, line.strip() == 'online')
    GLib.idle_add(loop.quit)

threading.Thread(target=commands, daemon=True).start()
print('ready', flush=True)
loop.run()
