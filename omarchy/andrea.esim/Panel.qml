import QtQuick
import QtQuick.Controls
import Quickshell
import Quickshell.Io
import qs.Ui
import qs.Commons
import qs.Commons as Commons

Panel {
  id: root
  moduleName: "andrea.esim"
  ipcTarget: "andrea.esim"
  manageIpc: false
  readonly property string esimctl: Quickshell.env("HOME") + "/.local/bin/esimctl"

  // esimctl --json modem
  property var mobile: ({})
  readonly property var modem: mobile.modem || null
  readonly property var conn: mobile.connection || ({})
  readonly property var settings: mobile.settings || ({})
  readonly property var slots: modem ? (modem.slots || []) : []
  readonly property bool radioOn: !!modem && ["disabled", "disabling", "failed", "locked", "initializing", ""].indexOf(modem.state || "") < 0
  readonly property bool registered: !!modem && !!modem.operator
  readonly property bool locked: !!modem && !!modem.unlock_required
  // esimctl --json status
  property var esim: ({ profiles: [], notifications: [] })
  readonly property var profiles: esim.profiles || []
  readonly property var notifications: esim.notifications || []
  // esimctl --json gps
  property var gps: ({})

  readonly property bool busy: actionProc.running
  property string pendingKey: ""
  property string actionMessage: ""
  property bool actionError: false
  property int selectedIndex: 0
  property bool cursorActive: false

  // Keyboard rows, top to bottom.
  readonly property int idxData: 0
  readonly property int idxRadio: 1
  readonly property int idxSlot: 2
  readonly property int idxProfile: idxSlot + slots.length
  readonly property int idxGps: idxProfile + profiles.length
  readonly property int idxNotif: notifications.length ? idxGps + 1 : -1
  readonly property int idxScan: idxGps + 1 + (notifications.length ? 1 : 0)
  readonly property int idxManager: idxScan + 1
  readonly property color fg: bar ? bar.foreground : Commons.Color.foreground

  readonly property string barIcon: {
    if (!modem) return "󰒨"
    if (locked) return "󰒨"
    if (!radioOn) return "󰒩"
    if (!registered) return "󰢿"
    var q = modem.signal || 0
    return q >= 67 ? "󰢾" : q >= 34 ? "󰢽" : q > 0 ? "󰢼" : "󰢿"
  }
  readonly property string summary: {
    if (busy) return "WORKING…"
    if (mobile.error) return "MODEM UNAVAILABLE"
    if (!modem) return "LOOKING FOR MODEM…"
    if (locked) return "SIM LOCKED · " + modem.unlock_required.toUpperCase() + " REQUIRED"
    if (!radioOn) return "RADIO OFF"
    if (!registered) return "SEARCHING FOR NETWORK"
    var s = modem.operator + " · " + (modem.access_tech || []).join("/").toUpperCase() + " · " + modem.signal + "%"
    if (modem.roaming) s += " · ROAMING"
    return s.toUpperCase()
  }

  function displayName(p) { return p.nickname || p.name || p.provider || p.iccid }
  function slotLabel(s) {
    if (!s.present) return "Empty"
    var t = s.type === "esim" ? "eSIM" : "SIM card"
    return s.operator ? t + " · " + s.operator : (s.type === "esim" && !s.iccid ? t + " · no profile enabled" : t)
  }
  function dataCaption() {
    if (conn.active) return "Connected · " + (conn.ipv4 || conn.ipv6 || "")
    var apn = settings.auto_apn
      ? (mobile.suggested_apn ? "APN " + mobile.suggested_apn.name + " (auto)" : "APN automatic")
      : "APN " + (settings.apn || "?")
    if (!registered) return apn + " · no network"
    return apn + (settings.roaming ? "" : " · no roaming")
  }
  function gpsCaption() {
    if (!gps.enabled) return "Off"
    if (!gps.valid) return "Searching · " + (gps.sats_in_view || 0) + " satellites in view"
    return gps.lat.toFixed(5) + ", " + gps.lon.toFixed(5) + " · " + (gps.mode || "").toUpperCase() + " · " + gps.sats_used + " sats"
  }

  function refreshModem() { if (!modemProc.running) modemProc.running = true }
  // eSIM and GPS reads share the modem's AT port: run them one after the
  // other (esimctl also serializes them with a lock).
  property bool gpsAfterEsim: false
  function refreshEsim() { if (!esimProc.running && !actionProc.running) esimProc.running = true }
  function refreshGps() {
    if (esimProc.running) { gpsAfterEsim = true; return }
    if (!gpsProc.running && !actionProc.running) gpsProc.running = true
  }
  function refreshAll() { refreshModem(); refreshEsim(); refreshGps() }
  function runAction(args, key) {
    if (busy) return false
    actionMessage = ""
    actionError = false
    pendingKey = key || ""
    actionProc.command = [root.esimctl, "--json"].concat(args)
    actionProc.running = true
    return true
  }
  function toggleProfile(p) { return runAction([p.enabled ? "disable" : "enable", p.iccid], p.iccid) }
  function toggleData() { return runAction(["data", conn.active ? "off" : "on"], "data") }
  function toggleRadio() { return runAction(["modem", radioOn ? "off" : "on"], "radio") }
  function toggleGps() { return runAction(["gps", gps.enabled ? "off" : "on"], "gps") }
  function selectSlot(s) { if (!s.active) runAction(["sim", "slot", String(s.index)], "slot" + s.index) }
  function openMap() {
    if (!gps.valid) return
    root.close()
    Quickshell.execDetached(["xdg-open", "https://www.openstreetmap.org/?mlat=" + gps.lat + "&mlon=" + gps.lon + "#map=17/" + gps.lat + "/" + gps.lon])
  }
  function openManager(extra) {
    root.close()
    Quickshell.execDetached(["omarchy-launch-or-focus-tui", "--app-id=org.omarchy.esimctl", root.esimctl, "tui"].concat(extra || []))
  }
  function scanQR() {
    // Select the QR first, so the terminal window can't cover it.
    root.close()
    if (!scanProc.running) scanProc.running = true
  }
  function activateSelected() {
    var i = selectedIndex
    if (i === idxData) toggleData()
    else if (i === idxRadio) toggleRadio()
    else if (i < idxProfile) selectSlot(slots[i - idxSlot])
    else if (i < idxGps) toggleProfile(profiles[i - idxProfile])
    else if (i === idxGps) toggleGps()
    else if (i === idxNotif) runAction(["notifications", "process"], "notifications")
    else if (i === idxScan) scanQR()
    else openManager()
  }
  onOpenedChanged: if (opened) { refreshAll(); cursorActive = false; selectedIndex = 0 }

  IpcHandler {
    target: "andrea.esim"
    function open() { root.open() }
    function close() { root.close() }
    function toggle() { root.toggle() }
    function status(): string { return JSON.stringify({ mobile: root.mobile, esim: root.esim, gps: root.gps }) }
    function scan() { root.scanQR() }
  }
  Process {
    id: modemProc
    command: [root.esimctl, "--json", "modem"]
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: {
        try { root.mobile = JSON.parse(text) }
        catch (e) { root.mobile = ({ error: "modem status unavailable" }) }
      }
    }
  }
  Process {
    id: esimProc
    command: [root.esimctl, "--json", "status"]
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: {
        try { root.esim = JSON.parse(text) }
        catch (e) { root.esim = ({ profiles: [], notifications: [], error: "eSIM unavailable" }) }
      }
    }
    onExited: function(exitCode, exitStatus) { if (root.gpsAfterEsim) { root.gpsAfterEsim = false; root.refreshGps() } }
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
          var result = JSON.parse(text)
          root.actionError = !!result.error
          root.actionMessage = result.error || result.message || (result.sent !== undefined ? result.sent + " notification(s) sent" : "")
        } catch (e) { root.actionMessage = "Operation failed"; root.actionError = true }
      }
    }
    onExited: function(exitCode, exitStatus) { root.pendingKey = ""; root.refreshAll() }
  }
  Process {
    id: scanProc
    command: [root.esimctl, "--json", "scan"]
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: {
        try {
          var result = JSON.parse(text)
          if (result.code) root.openManager(["--code", result.code])
          else if (result.error && result.error !== "selection cancelled")
            Quickshell.execDetached(["notify-send", "-a", "eSIM", "No eSIM QR code found", result.error])
        } catch (e) {}
      }
    }
  }
  Timer {
    // ModemManager queries are cheap: keep the bar icon current.
    interval: root.opened ? 4000 : 30000
    running: true
    repeat: true
    triggeredOnStart: true
    onTriggered: root.refreshModem()
  }
  Timer {
    // eSIM and GPS reads hold the modem's AT port: only while the panel is open.
    interval: 5000
    running: root.opened
    repeat: true
    onTriggered: { if (root.gps.enabled) root.refreshGps() }
  }
  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight
  BarIconButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    text: root.barIcon
    active: !!root.conn.active
    useActiveColor: true
    tooltipText: "Mobile · " + (root.conn.active ? "connected · " : "") + root.summary.toLowerCase()
    onPressed: root.toggle()
  }

  KeyboardPanel {
    id: panel
    anchorItem: button
    owner: root
    bar: root.bar
    open: root.opened
    focusTarget: keys
    contentWidth: panel.fittedContentWidth(Style.space(380))
    contentHeight: panel.fittedContentHeight(content.implicitHeight, Style.space(640))
    PanelKeyCatcher {
      id: keys
      anchors.fill: parent
      onCloseRequested: root.close()
      onTabRequested: function(direction) { root.switchPanel(direction) }
      onMoveRequested: function(dx, dy) {
        root.cursorActive = true
        if (dy) root.selectedIndex = Math.max(0, Math.min(root.idxManager, root.selectedIndex + dy))
      }
      onActivateRequested: if (!root.busy) root.activateSelected()
    }
    ScrollView {
      id: scroll
      anchors.fill: parent
      clip: true
      contentWidth: availableWidth
      ScrollBar.horizontal.policy: ScrollBar.AlwaysOff
      ScrollBar.vertical.policy: content.implicitHeight > height ? ScrollBar.AsNeeded : ScrollBar.AlwaysOff
    Column {
      id: content
      width: scroll.availableWidth
      spacing: Style.space(10)
      Row {
        width: parent.width
        spacing: Style.space(12)
        Text {
          text: root.barIcon
          color: root.fg
          font.family: root.bar.fontFamily
          font.pixelSize: Style.font.display
          anchors.verticalCenter: parent.verticalCenter
        }
        Column {
          spacing: Style.space(2)
          width: parent.width - Style.space(60)
          Text {
            text: "Mobile"
            color: root.fg
            font.family: root.bar.fontFamily
            font.pixelSize: Style.font.title
            font.bold: true
          }
          Text {
            width: parent.width
            text: root.summary
            textFormat: Text.PlainText
            elide: Text.ElideRight
            color: Qt.darker(root.fg, 1.4)
            font.family: root.bar.fontFamily
            font.pixelSize: Style.font.caption
          }
        }
      }
      PanelSeparator { foreground: root.fg }

      MobileRow {
        width: content.width
        fg: root.fg
        fontFamily: root.bar.fontFamily
        toggleEnabled: !root.busy
        cursor: root.cursorActive && root.selectedIndex === (root.idxData)
        onHoveredRow: { root.cursorActive = true; root.selectedIndex = (root.idxData) }
        title: "Mobile data"
        caption: root.dataCaption()
        checked: !!root.conn.active
        toggleBusy: root.busy && root.pendingKey === "data"
        onToggled: root.toggleData()
      }
      MobileRow {
        width: content.width
        fg: root.fg
        fontFamily: root.bar.fontFamily
        toggleEnabled: !root.busy
        cursor: root.cursorActive && root.selectedIndex === (root.idxRadio)
        onHoveredRow: { root.cursorActive = true; root.selectedIndex = (root.idxRadio) }
        title: "Cellular radio"
        caption: root.modem ? (root.modem.model + " · " + (root.modem.state || "")) : (root.mobile.error || "")
        checked: root.radioOn
        toggleBusy: root.busy && root.pendingKey === "radio"
        onToggled: root.toggleRadio()
      }
      Text {
        width: parent.width
        visible: root.locked
        text: "The SIM is locked. Open the manager to enter the " + (root.modem && root.modem.unlock_required || "PIN").toUpperCase() + "."
        wrapMode: Text.WordWrap
        color: Commons.Color.urgent
        font.family: root.bar.fontFamily
        font.pixelSize: Style.font.bodySmall
      }

      PanelSectionHeader {
        text: "SIM SLOTS"
        foreground: root.fg
        fontFamily: root.bar.fontFamily
        visible: root.slots.length > 0
      }
      Repeater {
        model: root.slots
        MobileRow {
          width: content.width
          fg: root.fg
          fontFamily: root.bar.fontFamily
          toggleEnabled: !root.busy
          cursor: root.cursorActive && root.selectedIndex === (root.idxSlot + index)
          onHoveredRow: { root.cursorActive = true; root.selectedIndex = (root.idxSlot + index) }
          required property var modelData
          required property int index
          title: "Slot " + modelData.index + (modelData.active ? "  ·  in use" : "")
          caption: root.slotLabel(modelData)
          checked: modelData.active
          showToggle: false
          onClicked: root.selectSlot(modelData)
        }
      }

      PanelSectionHeader {
        text: "ESIM PROFILES"
        foreground: root.fg
        fontFamily: root.bar.fontFamily
      }
      Text {
        width: parent.width
        visible: root.profiles.length === 0
        text: root.esim.error ? root.esim.error : "No eSIM profiles. Scan your carrier's QR code to add one."
        textFormat: Text.PlainText
        wrapMode: Text.WordWrap
        color: Qt.darker(root.fg, 1.4)
        font.family: root.bar.fontFamily
        font.pixelSize: Style.font.bodySmall
      }
      Repeater {
        model: root.profiles
        MobileRow {
          width: content.width
          fg: root.fg
          fontFamily: root.bar.fontFamily
          toggleEnabled: !root.busy
          cursor: root.cursorActive && root.selectedIndex === (root.idxProfile + index)
          onHoveredRow: { root.cursorActive = true; root.selectedIndex = (root.idxProfile + index) }
          required property var modelData
          required property int index
          title: root.displayName(modelData)
          caption: [modelData.provider, "…" + modelData.iccid.slice(-4),
                    modelData.class !== "operational" ? modelData["class"].toUpperCase() : ""]
                   .filter(function(s) { return !!s }).join(" · ")
          checked: modelData.enabled
          toggleBusy: root.busy && root.pendingKey === modelData.iccid
          onToggled: root.toggleProfile(modelData)
        }
      }

      PanelSectionHeader {
        text: "LOCATION"
        foreground: root.fg
        fontFamily: root.bar.fontFamily
      }
      MobileRow {
        width: content.width
        fg: root.fg
        fontFamily: root.bar.fontFamily
        toggleEnabled: !root.busy
        cursor: root.cursorActive && root.selectedIndex === (root.idxGps)
        onHoveredRow: { root.cursorActive = true; root.selectedIndex = (root.idxGps) }
        title: "GPS" + (root.gps.valid ? "  ·  open map 󰍎" : "")
        caption: root.gpsCaption()
        checked: !!root.gps.enabled
        toggleBusy: root.busy && root.pendingKey === "gps"
        onToggled: root.toggleGps()
        MouseArea {
          // The title opens the map once there is a fix.
          visible: !!root.gps.valid
          x: Style.space(8)
          width: parent.width / 2
          height: parent.height
          onClicked: root.openMap()
        }
      }

      Button {
        width: parent.width
        visible: root.notifications.length > 0
        text: "Send " + root.notifications.length + " pending eSIM notification" + (root.notifications.length === 1 ? "" : "s")
        iconText: "󰒊"
        foreground: root.fg
        fontFamily: root.bar.fontFamily
        bordered: true
        enabled: !root.busy
        hasCursor: root.cursorActive && root.selectedIndex === root.idxNotif
        onHovered: function(on) { if (on) { root.cursorActive = true; root.selectedIndex = root.idxNotif } }
        onClicked: root.runAction(["notifications", "process"], "notifications")
      }
      Text {
        width: parent.width
        visible: root.actionMessage !== ""
        text: root.actionMessage
        textFormat: Text.PlainText
        wrapMode: Text.WordWrap
        color: root.actionError ? Commons.Color.urgent : root.fg
        font.family: root.bar.fontFamily
        font.pixelSize: Style.font.bodySmall
      }
      PanelSeparator { foreground: root.fg }
      Button {
        width: parent.width
        text: "Scan eSIM QR code…"
        iconText: "󰐲"
        foreground: root.fg
        fontFamily: root.bar.fontFamily
        bordered: true
        hasCursor: root.cursorActive && root.selectedIndex === root.idxScan
        onHovered: function(on) { if (on) { root.cursorActive = true; root.selectedIndex = root.idxScan } }
        onClicked: root.scanQR()
      }
      Button {
        width: parent.width
        text: "Open mobile manager…"
        iconText: "󰒓"
        foreground: root.fg
        fontFamily: root.bar.fontFamily
        bordered: true
        hasCursor: root.cursorActive && root.selectedIndex === root.idxManager
        onHovered: function(on) { if (on) { root.cursorActive = true; root.selectedIndex = root.idxManager } }
        onClicked: root.openManager()
      }
    }
    }
  }
}
