import QtQuick
import Quickshell
import Quickshell.Io
import qs.Ui
import qs.Commons
import qs.Commons as Commons

// Mobile data section of the Connections panel: the everyday controls only,
// data on/off with signal strength, and messages. Everything else -- SIM or
// eSIM, eSIM profiles, GPS, APN, PIN, roaming -- lives in `esimctl tui`.
Column {
  id: root
  required property var bar
  required property var owner
  property bool opened: false
  readonly property string esimctl: Quickshell.env("HOME") + "/.local/bin/esimctl"
  readonly property color fg: bar ? bar.foreground : Commons.Color.foreground

  // esimctl --json modem / gps
  property var mobile: ({})
  property var gps: ({})
  readonly property var modem: mobile.modem || null
  readonly property var conn: mobile.connection || ({})
  readonly property bool available: !!modem
  readonly property bool radioOn: !!modem && ["disabled", "disabling", "failed", "locked", "initializing", ""].indexOf(modem.state || "") < 0
  readonly property bool locked: !!modem && !!modem.unlock_required
  readonly property bool registered: !!modem && !!modem.operator
  readonly property bool connected: !!conn.active
  readonly property bool gpsOn: !!gps.enabled
  readonly property int signal: modem ? (modem.signal || 0) : 0
  readonly property int unread: mobile.unread || 0
  readonly property bool busy: actionProc.running
  property string message: ""

  // Cellular signal bars (0-3), also used by the bar icon when mobile data is
  // the only link.
  readonly property string icon: {
    if (!modem || locked) return "\u{F04A8}"      // SIM with alert
    if (!radioOn) return "\u{F04A9}"              // SIM off
    if (!registered) return "\u{F08BF}"            // no bars
    return signal >= 67 ? "\u{F08BE}" : signal >= 34 ? "\u{F08BD}" : signal > 0 ? "\u{F08BC}" : "\u{F08BF}"
  }

  readonly property string title: {
    if (!modem) return "Mobile data"
    if (locked) return "SIM locked"
    if (!radioOn) return "Radio off"
    if (!registered) return "No network"
    return modem.operator + " · " + (modem.access_tech || []).join("/").toUpperCase() + " · " + signal + "%"
  }

  readonly property string status: {
    if (mobile.error) return mobile.error
    if (locked) return "Enter the PIN in Mobile settings"
    if (!radioOn) return "Turn the radio on in Mobile settings"
    if (connected) {
      var s = "Connected" + (conn.ipv4 ? " · " + conn.ipv4 : "")
      if (owner.kind !== "disconnected") s += " · backup"
      if (modem.roaming) s += " · roaming"
      return s
    }
    return registered ? "Off" : "Searching for a network…"
  }

  // ---- keyboard cursor: row 0 is data, row 1 messages.
  readonly property int rowCount: available ? 2 : 0
  property int cursor: 0
  readonly property bool focused: owner.cursorActive && owner.focusSection === "mobile"
  function enter(fromTop) { cursor = fromTop ? 0 : rowCount - 1 }
  function moveCursor(dy) {
    var next = cursor + dy
    if (next < 0 || next >= rowCount) return false
    cursor = next
    return true
  }
  function moveSide(dx) {}
  function hover(row) {
    owner.cursorActive = true
    owner.focusSection = "mobile"
    cursor = row
  }
  function activate() {
    if (cursor === 0) toggleData()
    else openMessages()
  }

  function refresh() {
    if (!modemProc.running) modemProc.running = true
    if (opened && !gpsProc.running && !actionProc.running) gpsProc.running = true
  }
  function toggleData() {
    if (busy || locked) return
    message = ""
    actionProc.command = [root.esimctl, "--json", "data", connected ? "off" : "on"]
    actionProc.running = true
  }
  function openMessages() {
    owner.close()
    Quickshell.execDetached(["omarchy-launch-or-focus-tui", "--app-id=org.omarchy.esimctl", root.esimctl, "tui", "--tab", "messages"])
  }

  onOpenedChanged: if (opened) refresh()

  Process {
    id: modemProc
    command: [root.esimctl, "--json", "modem"]
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: { try { root.mobile = JSON.parse(text) } catch (e) { root.mobile = ({}) } }
    }
  }
  Process {
    id: gpsProc
    command: [root.esimctl, "--json", "gps"]
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: { try { root.gps = JSON.parse(text) } catch (e) { root.gps = ({}) } }
    }
  }
  Process {
    id: actionProc
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: {
        try { var r = JSON.parse(text); root.message = r.error || "" } catch (e) { root.message = "Operation failed" }
      }
    }
    onExited: root.refresh()
  }
  Timer {
    // ModemManager reads are cheap; the bar icon follows the signal.
    interval: root.opened ? 3000 : 20000
    running: true
    repeat: true
    triggeredOnStart: true
    onTriggered: root.refresh()
  }

  visible: available
  spacing: Style.space(6)

  PanelSeparator { width: parent.width; foreground: root.fg }

  PanelSectionHeader {
    text: "MOBILE DATA"
    foreground: root.fg
    fontFamily: root.bar.fontFamily
  }

  SectionRow {
    width: parent.width
    fg: root.fg
    fontFamily: root.bar.fontFamily
    icon: root.icon
    title: root.title
    caption: root.status
    captionColor: root.locked ? root.bar.urgent : Qt.darker(root.fg, 1.4)
    checked: root.connected
    toggleBusy: root.busy
    toggleEnabled: !root.busy && !root.locked && root.radioOn
    cursor: root.focused && root.cursor === 0
    onHoveredRow: root.hover(0)
    onToggled: root.toggleData()
  }

  SectionRow {
    width: parent.width
    fg: root.fg
    fontFamily: root.bar.fontFamily
    icon: "\u{F0369}"
    title: root.unread > 0 ? "Messages · " + root.unread + " unread" : "Messages"
    caption: (root.mobile.messages || 0) === 0 ? "No messages"
      : root.mobile.messages + (root.mobile.messages === 1 ? " message" : " messages")
    showToggle: false
    cursor: root.focused && root.cursor === 1
    onHoveredRow: root.hover(1)
    onClicked: root.openMessages()
  }

  Text {
    width: parent.width
    visible: root.message !== ""
    text: root.message
    textFormat: Text.PlainText
    wrapMode: Text.WordWrap
    color: root.bar.urgent
    font.family: root.bar.fontFamily
    font.pixelSize: Style.font.caption
  }
}
