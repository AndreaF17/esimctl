import QtQuick
import Quickshell
import Quickshell.Io
import qs.Ui
import qs.Commons
import qs.Commons as Commons

// Mobile data section of the Connections overview: the everyday controls
// only (data on/off, which SIM, eSIM profile, GPS). Configuration -- APN, PIN,
// roaming, downloads -- lives in `esimctl` and `esimctl tui`.
Column {
  id: root
  required property var bar
  required property var owner
  property bool opened: false
  readonly property string esimctl: Quickshell.env("HOME") + "/.local/bin/esimctl"
  readonly property color fg: bar ? bar.foreground : Commons.Color.foreground

  // esimctl --json modem / status / gps
  property var mobile: ({})
  property var esim: ({ profiles: [] })
  property var gps: ({})
  readonly property var modem: mobile.modem || null
  readonly property var conn: mobile.connection || ({})
  readonly property var slots: modem ? (modem.slots || []) : []
  readonly property var activeSlot: {
    for (var i = 0; i < slots.length; i++) if (slots[i].active) return slots[i]
    return null
  }
  readonly property bool esimActive: !!activeSlot && activeSlot.type === "esim"
  readonly property var profiles: esimActive ? (esim.profiles || []) : []
  readonly property bool available: !!modem
  readonly property bool radioOn: !!modem && ["disabled", "disabling", "failed", "locked", "initializing", ""].indexOf(modem.state || "") < 0
  readonly property bool locked: !!modem && !!modem.unlock_required
  readonly property bool connected: !!conn.active
  readonly property bool busy: actionProc.running
  property string pendingKey: ""
  property string message: ""
  property bool messageError: false

  // Signal bars, also used by the bar icon when mobile data is the only link.
  readonly property string icon: {
    if (!modem || locked) return "󰒨"
    if (!radioOn) return "󰒩"
    if (!modem.operator) return "󰢿"
    var q = modem.signal || 0
    return q >= 67 ? "󰢾" : q >= 34 ? "󰢽" : q > 0 ? "󰢼" : "󰢿"
  }

  readonly property string status: {
    if (mobile.error) return mobile.error
    if (!modem) return ""
    if (locked) return "SIM locked: enter the PIN in Mobile settings"
    if (!radioOn) return "Radio off"
    if (!modem.operator) return activeSlot && !activeSlot.iccid && activeSlot.type === "esim"
      ? "No eSIM profile enabled" : "Searching for a network…"
    var s = modem.operator + " · " + (modem.access_tech || []).join("/").toUpperCase() + " · " + modem.signal + "%"
    if (modem.roaming) s += " · roaming"
    if (connected) s += owner.kind !== "disconnected" ? " · standby behind " + (owner.kind === "wifi" ? "Wi-Fi" : "Ethernet") : " · in use"
    return s
  }

  // ---- keyboard cursor: rows are data, slots (if more than one), eSIM
  // profiles (while the eSIM is in use) and GPS.
  readonly property bool hasSlotRow: slots.length > 1
  readonly property int slotRow: hasSlotRow ? 1 : -1
  readonly property int firstProfileRow: 1 + (hasSlotRow ? 1 : 0)
  readonly property int messagesRow: firstProfileRow + profiles.length
  readonly property int gpsRow: messagesRow + 1
  readonly property int rowCount: available ? gpsRow + 1 : 0
  readonly property int unread: mobile.unread || 0
  property int cursor: 0
  property int slotCursor: 0
  readonly property bool focused: owner.cursorActive && owner.focusSection === "mobile"

  function enter(fromTop) { cursor = fromTop ? 0 : rowCount - 1; syncSlotCursor() }
  // Returns false when the move leaves the section.
  function moveCursor(dy) {
    var next = cursor + dy
    if (next < 0 || next >= rowCount) return false
    cursor = next
    return true
  }
  function moveSide(dx) {
    if (cursor === slotRow) slotCursor = Math.max(0, Math.min(slots.length - 1, slotCursor + dx))
  }
  function syncSlotCursor() {
    for (var i = 0; i < slots.length; i++) if (slots[i].active) slotCursor = i
  }
  function hover(row) {
    owner.cursorActive = true
    owner.focusSection = "mobile"
    cursor = row
  }
  function activate() {
    if (busy) return
    if (cursor === 0) toggleData()
    else if (cursor === slotRow) selectSlot(slots[slotCursor])
    else if (cursor === gpsRow) toggleGps()
    else if (cursor === messagesRow) openMessages()
    else toggleProfile(profiles[cursor - firstProfileRow])
  }

  function refresh() {
    if (!modemProc.running) modemProc.running = true
    if (opened && !gpsProc.running && !actionProc.running) gpsProc.running = true
  }
  function refreshEsim() {
    if (esimActive && !esimProc.running && !actionProc.running) esimProc.running = true
  }
  function run(args, key) {
    if (busy) return
    message = ""
    messageError = false
    pendingKey = key
    actionProc.command = [root.esimctl, "--json"].concat(args)
    actionProc.running = true
  }
  function openMessages() {
    owner.close()
    Quickshell.execDetached(["omarchy-launch-or-focus-tui", "--app-id=org.omarchy.esimctl", root.esimctl, "tui", "--tab", "messages"])
  }
  function toggleData() { run(["data", connected ? "off" : "on"], "data") }
  function toggleGps() { run(["gps", gps.enabled ? "off" : "on"], "gps") }
  function toggleProfile(p) { run([p.enabled ? "disable" : "enable", p.iccid], p.iccid) }
  function selectSlot(s) { if (s && !s.active && s.present) run(["sim", "slot", String(s.index)], "slot") }
  function satellites(n) { return n + (n === 1 ? " satellite" : " satellites") }
  function slotLabel(s) {
    if (!s.present) return "Slot " + s.index + " · empty"
    var kind = s.type === "esim" ? "eSIM" : "SIM"
    return s.operator ? kind + " · " + s.operator : kind
  }

  onOpenedChanged: if (opened) { refresh(); refreshEsim(); syncSlotCursor() }
  onEsimActiveChanged: refreshEsim()

  Process {
    id: modemProc
    command: [root.esimctl, "--json", "modem"]
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: {
        try { root.mobile = JSON.parse(text) } catch (e) { root.mobile = ({}) }
      }
    }
  }
  Process {
    id: esimProc
    command: [root.esimctl, "--json", "status"]
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: { try { root.esim = JSON.parse(text) } catch (e) { root.esim = ({ profiles: [] }) } }
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
        try {
          var r = JSON.parse(text)
          root.messageError = !!r.error
          root.message = r.error || ""
        } catch (e) { root.message = "Operation failed"; root.messageError = true }
      }
    }
    onExited: function() {
      var key = root.pendingKey
      root.pendingKey = ""
      root.refresh()
      if (key !== "data" && key !== "gps") root.refreshEsim()
    }
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
    title: root.connected ? "Connected" + (root.conn.ipv4 ? " · " + root.conn.ipv4 : "") : "Mobile data"
    caption: root.status
    captionColor: root.locked ? root.bar.urgent : Qt.darker(root.fg, 1.4)
    checked: root.connected
    toggleBusy: root.busy && root.pendingKey === "data"
    toggleEnabled: !root.busy && !root.locked
    cursor: root.focused && root.cursor === 0
    onHoveredRow: root.hover(0)
    onToggled: root.toggleData()
  }

  Row {
    id: slotRow
    visible: root.hasSlotRow
    width: parent.width
    spacing: Style.space(6)
    readonly property real cellWidth: (width - spacing * (root.slots.length - 1)) / Math.max(1, root.slots.length)
    Repeater {
      model: root.slots
      Button {
        required property var modelData
        required property int index
        width: slotRow.cellWidth
        text: root.slotLabel(modelData)
        fontSize: Style.font.bodySmall
        foreground: root.fg
        fontFamily: root.bar.fontFamily
        bordered: true
        active: modelData.active
        enabled: !root.busy && modelData.present
        hasCursor: root.focused && root.cursor === root.slotRow && root.slotCursor === index
        tooltipText: modelData.active ? "In use" : "Switch to this SIM (the modem restarts)"
        onHovered: function(on) { if (on) { root.hover(root.slotRow); root.slotCursor = index } }
        onClicked: root.selectSlot(modelData)
      }
    }
  }

  Repeater {
    model: root.profiles
    SectionRow {
      required property var modelData
      required property int index
      width: root.width
      fg: root.fg
      fontFamily: root.bar.fontFamily
      title: modelData.nickname || modelData.name || modelData.provider || modelData.iccid
      caption: "eSIM profile" + (modelData.provider ? " · " + modelData.provider : "")
      checked: modelData.enabled
      toggleBusy: root.busy && root.pendingKey === modelData.iccid
      toggleEnabled: !root.busy
      cursor: root.focused && root.cursor === root.firstProfileRow + index
      onHoveredRow: root.hover(root.firstProfileRow + index)
      onToggled: root.toggleProfile(modelData)
    }
  }

  SectionRow {
    width: parent.width
    fg: root.fg
    fontFamily: root.bar.fontFamily
    title: root.unread > 0 ? "Messages · " + root.unread + " unread" : "Messages"
    caption: (root.mobile.messages || 0) === 0 ? "No messages" : root.mobile.messages + (root.mobile.messages === 1 ? " message" : " messages")
    captionColor: Qt.darker(root.fg, 1.4)
    showToggle: false
    cursor: root.focused && root.cursor === root.messagesRow
    onHoveredRow: root.hover(root.messagesRow)
    onClicked: root.openMessages()
  }

  SectionRow {
    width: parent.width
    fg: root.fg
    fontFamily: root.bar.fontFamily
    title: "GPS"
    caption: !root.gps.enabled ? "Off"
      : !root.gps.valid ? "Searching · " + root.satellites(root.gps.sats_in_view || 0) + " in view"
      : root.gps.lat.toFixed(5) + ", " + root.gps.lon.toFixed(5) + " · " + root.satellites(root.gps.sats_used)
    checked: !!root.gps.enabled
    toggleBusy: root.busy && root.pendingKey === "gps"
    toggleEnabled: !root.busy
    cursor: root.focused && root.cursor === root.gpsRow
    onHoveredRow: root.hover(root.gpsRow)
    onToggled: root.toggleGps()
  }

  Text {
    width: parent.width
    visible: root.message !== ""
    text: root.message
    textFormat: Text.PlainText
    wrapMode: Text.WordWrap
    color: root.messageError ? root.bar.urgent : root.fg
    font.family: root.bar.fontFamily
    font.pixelSize: Style.font.caption
  }
}
