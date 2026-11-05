import QtQuick
import qs.Ui
import qs.Commons

// A row of the overview sections: optional icon, title, caption and an
// optional switch. Clicking anywhere on the row does what the switch does.
// Active rows aren't filled: the switch already shows the state.
CursorSurface {
  id: r
  property string icon
  property string title
  property string caption
  property color captionColor: Qt.darker(fg, 1.4)
  property bool checked
  property bool toggleBusy
  property bool toggleEnabled: true
  property bool showToggle: true
  // Without a switch, an optional glyph at the right edge (e.g. a chevron
  // saying the row opens something).
  property string trailingIcon: ""
  property bool cursor: false
  property color fg
  property string fontFamily
  signal toggled()
  signal clicked()
  signal hoveredRow()
  implicitHeight: labels.implicitHeight + Style.space(14)
  foreground: fg
  current: false
  hasCursor: cursor
  MouseArea {
    anchors.fill: parent
    hoverEnabled: true
    cursorShape: Qt.PointingHandCursor
    onEntered: r.hoveredRow()
    onClicked: r.showToggle ? (r.toggleEnabled ? r.toggled() : null) : r.clicked()
  }
  Text {
    id: iconText
    visible: r.icon !== ""
    text: r.icon
    color: r.fg
    font.family: r.fontFamily
    font.pixelSize: Style.font.title
    anchors.left: parent.left
    anchors.leftMargin: Style.space(10)
    anchors.verticalCenter: parent.verticalCenter
  }
  Column {
    id: labels
    anchors.left: iconText.visible ? iconText.right : parent.left
    anchors.leftMargin: Style.space(10)
    anchors.verticalCenter: parent.verticalCenter
    width: parent.width - (r.showToggle ? sw.width + Style.space(8) : trailing.visible ? trailing.width + Style.space(8) : 0) - Style.space(20)
      - (iconText.visible ? iconText.width + Style.space(10) : 0)
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
  Text {
    id: trailing
    visible: !r.showToggle && r.trailingIcon !== ""
    text: r.trailingIcon
    color: r.cursor ? r.fg : Qt.darker(r.fg, 1.4)
    font.family: r.fontFamily
    font.pixelSize: Style.font.body
    anchors.right: parent.right
    anchors.rightMargin: Style.space(12)
    anchors.verticalCenter: parent.verticalCenter
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
