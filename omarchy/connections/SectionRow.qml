import QtQuick
import qs.Ui
import qs.Commons

// A row of the overview sections: title, caption and an optional switch.
// Clicking anywhere on the row does what the switch does.
CursorSurface {
  id: r
  property string title
  property string caption
  property color captionColor: Qt.darker(fg, 1.4)
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
  implicitHeight: labels.implicitHeight + Style.space(14)
  foreground: fg
  current: checked && showToggle
  hasCursor: cursor
  MouseArea {
    anchors.fill: parent
    hoverEnabled: true
    cursorShape: Qt.PointingHandCursor
    onEntered: r.hoveredRow()
    onClicked: r.showToggle ? (r.toggleEnabled ? r.toggled() : null) : r.clicked()
  }
  Column {
    id: labels
    anchors.left: parent.left
    anchors.leftMargin: Style.space(10)
    anchors.verticalCenter: parent.verticalCenter
    width: parent.width - (r.showToggle ? sw.width + Style.space(8) : 0) - Style.space(20)
    spacing: Style.space(2)
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
      color: r.captionColor
      font.family: r.fontFamily
      font.pixelSize: Style.font.caption
    }
  }
  ToggleSwitch {
    id: sw
    visible: r.showToggle
    anchors.right: parent.right
    anchors.rightMargin: Style.space(10)
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
