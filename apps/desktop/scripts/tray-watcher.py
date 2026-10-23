#!/usr/bin/env python3
"""Private D-Bus watcher fixture. Run only inside dbus-run-session."""
import json
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

    item = None

    @dbus.service.method('org.kde.StatusNotifierWatcher', in_signature='s', sender_keyword='sender')
    def RegisterStatusNotifierItem(self, service, sender=None):
        self.item = (sender, str(service)) if service.startswith('/') else (str(service), '/StatusNotifierItem')

    @dbus.service.method('org.freedesktop.DBus.Properties', in_signature='ss', out_signature='v')
    def Get(self, _interface, prop):
        if prop == 'IsStatusNotifierHostRegistered':
            return dbus.Boolean(self.host)
        raise dbus.DBusException('Unknown property')

watcher = Watcher(bus, '/StatusNotifierWatcher')

def set_host(value):
    watcher.host = value
    return False

def inspect(command):
    try:
        service, path = watcher.item
        item = bus.get_object(service, path, introspect=False)
        menu = dbus.Interface(bus.get_object(service, '/MenuBar', introspect=False), 'com.canonical.dbusmenu')
        _, layout = menu.GetLayout(0, -1, dbus.Array([], signature="s"), timeout=2)
        rows = [{'id': int(row[0]), 'label': str(row[1].get('label', '')),
                 'enabled': bool(row[1].get('enabled', True))} for row in layout[2]]
        if command == 'activate':
            dbus.Interface(item, 'org.kde.StatusNotifierItem').Activate(0, 0, timeout=2)
        elif command.startswith('click:'):
            row = next(r for r in rows if r['label'] == command[6:])
            menu.Event(row['id'], 'clicked', dbus.Int32(0, variant_level=1), dbus.UInt32(0), timeout=2)
        print(json.dumps(rows), flush=True)
    except Exception as error:
        print(json.dumps({'error': str(error)}), flush=True)
    return False

def commands():
    for line in sys.stdin:
        command = line.strip()
        if command in ('online', 'offline'):
            GLib.idle_add(set_host, command == 'online')
        else:
            GLib.idle_add(inspect, command)
    GLib.idle_add(loop.quit)

threading.Thread(target=commands, daemon=True).start()
print('ready', flush=True)
loop.run()
