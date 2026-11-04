import QtQuick
import qs.Ui
import qs.Commons

// A panel row with a title, a caption and an optional toggle switch.
CursorSurface {
  id: r
  property string title
  property string caption
  property bool checked
  property bool toggleBusy
  property bool toggleEnabled: true
  property bool showToggle: true
  property bool cursor: false
  property color fg
  property string fontFamily
  signal toggled()
  signal clicked()
  signal hoveredRow()
  implicitHeight: labels.implicitHeight + Style.space(16)
  foreground: fg
  current: checked
  hasCursor: cursor
  MouseArea {
    anchors.fill: parent
    hoverEnabled: true
    onEntered: r.hoveredRow()
    onClicked: r.showToggle ? r.toggled() : r.clicked()
  }
  Column {
    id: labels
    anchors.left: parent.left
    anchors.leftMargin: Style.space(8)
    anchors.verticalCenter: parent.verticalCenter
    width: parent.width - (r.showToggle ? sw.width : 0) - Style.space(24)
    spacing: Style.space(3)
    Text {
      width: parent.width
      text: r.title
      textFormat: Text.PlainText
      elide: Text.ElideRight
      color: r.fg
      font.family: r.fontFamily
      font.pixelSize: Style.font.body
    }
    Text {
      width: parent.width
      visible: text !== ""
      text: r.caption
      textFormat: Text.PlainText
      elide: Text.ElideRight
      color: Qt.darker(r.fg, 1.4)
      font.family: r.fontFamily
      font.pixelSize: Style.font.caption
    }
  }
  ToggleSwitch {
    id: sw
    visible: r.showToggle
    anchors.right: parent.right
    anchors.rightMargin: Style.space(8)
    anchors.verticalCenter: parent.verticalCenter
    foreground: r.fg
    checked: r.checked
    busy: r.toggleBusy
    enabled: r.toggleEnabled
    hasCursor: r.cursor
    onHovered: function(on) { if (on) r.hoveredRow() }
    onToggled: r.toggled()
  }
}
