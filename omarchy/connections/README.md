# Mobile section for the Omarchy Connections panel

`MobileSection.qml` is the mobile data part of a Connections panel that shows
Wi-Fi, mobile data and VPN on one page. It covers the everyday controls only:
data on/off, SIM slot, eSIM profile, messages and GPS; configuration (APN,
PIN, roaming, eSIM downloads) stays in `esimctl` and `esimctl tui`.

To use it, clone the network widget (`omarchy plugin clone omarchy.network`),
copy both files next to its `Panel.qml`, and add:

    MobileSection {
      id: mobileSection
      width: parent.width
      bar: root.bar
      owner: root
      opened: root.opened
    }

The owner must provide `cursorActive`, `focusSection`, `kind` (`"wifi"`,
`"ethernet"` or `"disconnected"`) and `close()`.
