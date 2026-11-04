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
  property var info: ({ profiles: [], notifications: [] })
  readonly property var profiles: info.profiles || []
  readonly property var notifications: info.notifications || []
  readonly property var activeProfile: {
    for (var i = 0; i < profiles.length; i++) if (profiles[i].enabled) return profiles[i]
    return null
  }
  readonly property bool busy: actionProc.running
  property string pendingKey: ""
  property string actionMessage: ""
  property bool actionError: false
  property int selectedIndex: 0
  property bool cursorActive: false
  // Keyboard rows: profiles, then [send notifications], scan, manager.
  readonly property int notifIndex: notifications.length ? profiles.length : -1
  readonly property int scanIndex: profiles.length + (notifications.length ? 1 : 0)
  readonly property int managerIndex: scanIndex + 1
  readonly property color fg: bar ? bar.foreground : Commons.Color.foreground

  function displayName(p) { return p.nickname || p.name || p.provider || p.iccid }
  function refresh() { if (!statusProc.running && !actionProc.running) statusProc.running = true }
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
    if (i < profiles.length) toggleProfile(profiles[i])
    else if (i === notifIndex) runAction(["notifications", "process"], "notifications")
    else if (i === scanIndex) scanQR()
    else openManager()
  }
  onOpenedChanged: if (opened) { refresh(); cursorActive = false; selectedIndex = 0 }

  IpcHandler {
    target: "andrea.esim"
    function open() { root.open() }
    function close() { root.close() }
    function toggle() { root.toggle() }
    function status(): string { return JSON.stringify(root.info) }
    function scan() { root.scanQR() }
  }
  Process {
    id: statusProc
    command: [root.esimctl, "--json", "status"]
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: {
        try { root.info = JSON.parse(text) }
        catch (e) { root.info = ({ profiles: [], notifications: [], error: "eSIM status unavailable" }) }
      }
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
        } catch (e) { root.actionMessage = "eSIM operation failed"; root.actionError = true }
      }
    }
    onExited: function(exitCode, exitStatus) { root.pendingKey = ""; root.refresh() }
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
    // Every read opens the modem's control port, so poll gently when closed.
    interval: root.opened ? 5000 : 120000
    running: true
    repeat: true
    triggeredOnStart: true
    onTriggered: root.refresh()
  }
  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight
  BarIconButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    text: root.info.error ? "󰒨" : (root.activeProfile ? "󰒧" : "󰒩")
    active: !!root.activeProfile
    useActiveColor: true
    tooltipText: root.activeProfile ? "eSIM · " + root.displayName(root.activeProfile) : "eSIM · No active profile"
    onPressed: root.toggle()
  }
  KeyboardPanel {
    id: panel
    anchorItem: button
    owner: root
    bar: root.bar
    open: root.opened
    focusTarget: keys
    contentWidth: panel.fittedContentWidth(Style.space(360))
    contentHeight: panel.fittedContentHeight(content.implicitHeight, Style.space(540))
    PanelKeyCatcher {
      id: keys
      anchors.fill: parent
      onCloseRequested: root.close()
      onTabRequested: function(direction) { root.switchPanel(direction) }
      onMoveRequested: function(dx, dy) {
        root.cursorActive = true
        if (dy) root.selectedIndex = Math.max(0, Math.min(root.managerIndex, root.selectedIndex + dy))
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
      spacing: Style.space(12)
      Row {
        width: parent.width
        spacing: Style.space(12)
        Text {
          text: root.info.error ? "󰒨" : (root.activeProfile ? "󰒧" : "󰒩")
          color: root.fg
          font.family: root.bar.fontFamily
          font.pixelSize: Style.font.display
          anchors.verticalCenter: parent.verticalCenter
        }
        Column {
          spacing: Style.space(2)
          width: parent.width - Style.space(60)
          Text {
            text: "eSIM"
            color: root.fg
            font.family: root.bar.fontFamily
            font.pixelSize: Style.font.title
            font.bold: true
          }
          Text {
            width: parent.width
            text: root.busy ? "WORKING…" : (root.activeProfile ? root.displayName(root.activeProfile).toUpperCase() : "NO ACTIVE PROFILE")
            textFormat: Text.PlainText
            elide: Text.ElideRight
            color: Qt.darker(root.fg, 1.4)
            font.family: root.bar.fontFamily
            font.pixelSize: Style.font.caption
          }
        }
      }
      PanelSeparator { foreground: root.fg }
      PanelSectionHeader {
        text: "PROFILES"
        foreground: root.fg
        fontFamily: root.bar.fontFamily
      }
      Text {
        width: parent.width
        visible: root.profiles.length === 0
        text: root.info.error ? "" : "No eSIM profiles installed.\nScan the QR code from your carrier to add one."
        textFormat: Text.PlainText
        wrapMode: Text.WordWrap
        color: Qt.darker(root.fg, 1.4)
        font.family: root.bar.fontFamily
        font.pixelSize: Style.font.bodySmall
      }
      Repeater {
        model: root.profiles
        CursorSurface {
          id: profileRow
          required property var modelData
          required property int index
          width: content.width
          implicitHeight: profileLabels.implicitHeight + Style.space(16)
          foreground: root.fg
          current: modelData.enabled
          hasCursor: root.cursorActive && root.selectedIndex === index
          Column {
            id: profileLabels
            anchors.left: parent.left
            anchors.leftMargin: Style.space(8)
            anchors.verticalCenter: parent.verticalCenter
            width: parent.width - profileToggle.width - Style.space(24)
            spacing: Style.space(3)
            Text {
              width: parent.width
              text: root.displayName(profileRow.modelData)
              textFormat: Text.PlainText
              elide: Text.ElideRight
              color: root.fg
              font.family: root.bar.fontFamily
              font.pixelSize: Style.font.body
            }
            Text {
              width: parent.width
              text: [profileRow.modelData.provider, "…" + profileRow.modelData.iccid.slice(-4),
                     profileRow.modelData.class !== "operational" ? profileRow.modelData.class.toUpperCase() : ""]
                    .filter(function(s) { return !!s }).join(" · ")
              textFormat: Text.PlainText
              elide: Text.ElideRight
              color: Qt.darker(root.fg, 1.4)
              font.family: root.bar.fontFamily
              font.pixelSize: Style.font.caption
            }
          }
          ToggleSwitch {
            id: profileToggle
            anchors.right: parent.right
            anchors.rightMargin: Style.space(8)
            anchors.verticalCenter: parent.verticalCenter
            foreground: root.fg
            checked: profileRow.modelData.enabled
            busy: root.busy && root.pendingKey === profileRow.modelData.iccid
            enabled: !root.busy
            hasCursor: profileRow.hasCursor
            onHovered: function(on) { if (on) { root.cursorActive = true; root.selectedIndex = profileRow.index } }
            onToggled: root.toggleProfile(profileRow.modelData)
          }
        }
      }
      Button {
        width: parent.width
        visible: root.notifications.length > 0
        text: "Send " + root.notifications.length + " pending notification" + (root.notifications.length === 1 ? "" : "s")
        iconText: "󰒊"
        foreground: root.fg
        fontFamily: root.bar.fontFamily
        bordered: true
        enabled: !root.busy
        hasCursor: root.cursorActive && root.selectedIndex === root.notifIndex
        onHovered: function(on) { if (on) { root.cursorActive = true; root.selectedIndex = root.notifIndex } }
        onClicked: root.runAction(["notifications", "process"], "notifications")
      }
      Text {
        width: parent.width
        visible: root.actionMessage !== "" || !!root.info.error
        text: root.actionMessage || root.info.error || ""
        textFormat: Text.PlainText
        wrapMode: Text.WordWrap
        color: root.actionError || root.info.error ? Commons.Color.urgent : root.fg
        font.family: root.bar.fontFamily
        font.pixelSize: Style.font.bodySmall
      }
      PanelSeparator { foreground: root.fg }
      Button {
        width: parent.width
        text: "Scan QR code…"
        iconText: "󰐲"
        foreground: root.fg
        fontFamily: root.bar.fontFamily
        bordered: true
        hasCursor: root.cursorActive && root.selectedIndex === root.scanIndex
        onHovered: function(on) { if (on) { root.cursorActive = true; root.selectedIndex = root.scanIndex } }
        onClicked: root.scanQR()
      }
      Button {
        width: parent.width
        text: "Open eSIM manager…"
        iconText: "󰒓"
        foreground: root.fg
        fontFamily: root.bar.fontFamily
        bordered: true
        hasCursor: root.cursorActive && root.selectedIndex === root.managerIndex
        onHovered: function(on) { if (on) { root.cursorActive = true; root.selectedIndex = root.managerIndex } }
        onClicked: root.openManager()
      }
    }
    }
  }
}
