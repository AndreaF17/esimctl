# esimctl

A manager for the cellular modem of Linux laptops: eSIM profiles (a native
SGP.22 LPA), physical SIM and slot switching, PIN, mobile data with automatic
APN selection, and GPS. It comes with a terminal UI and an
[Omarchy](https://omarchy.org) bar widget.

Developed on a ThinkPad X1 Carbon Gen 12 with a Quectel EM160R-GL.

## How it works

- **eSIM**: esimctl implements the ES10 (eUICC) and ES9+ (SM-DP+) interfaces
  itself, with no lpac. It talks to the eUICC through logical channels,
  either over MBIM (`/dev/wwan*mbim*`) or, when ModemManager owns the MBIM
  port, over the AT port with `AT+CCHO`/`AT+CGLA`.
- **Modem, SIM and data**: through ModemManager (`mmcli`) and NetworkManager
  (`nmcli`). esimctl creates a "Mobile broadband" connection and picks the APN
  from `mobile-broadband-provider-info`, matching the SIM's home network and
  its operator name (virtual operators share their host's network code).
- **GPS**: Quectel `AT+QGPS*` commands.

## Install

    sudo pacman -S modemmanager mobile-broadband-provider-info
    sudo systemctl enable --now ModemManager
    ./install.sh

`install.sh` builds `~/.local/bin/esimctl`, installs the `andrea.esim` bar
widget, and (once, via pkexec) `contrib/70-esimctl.rules`, which gives the
logged-in user the modem's AT and NMEA ports and tells ModemManager to leave
the AT port to esimctl.

### FCC unlock (Lenovo)

Lenovo ships these modems with the radio locked until the OS unlocks it. If
`esimctl modem on` fails with "Invalid transition", enable ModemManager's
Quectel unlock script for your modem's PCI id (`lspci -nn`), for example:

    sudo ln -s /usr/share/ModemManager/fcc-unlock.available.d/2c7c \
      /etc/ModemManager/fcc-unlock.d/1eac:100d
    sudo systemctl restart ModemManager

## Use

    esimctl tui                       # interactive; `s` scans a QR on screen
    esimctl modem                     # state, network, slots, data, APN
    esimctl modem on|off|signal
    esimctl sim slot 1                # physical SIM; slot 2 is the eSIM here
    esimctl sim pin 1234
    esimctl data on|off
    esimctl data apn auto             # or: esimctl data apn <apn> [user] [pass]
    esimctl data roaming on
    esimctl gps on && esimctl gps
    esimctl download 'LPA:1$smdp.example.com$MATCHING-ID'
    esimctl list | enable | disable | delete | nickname
    esimctl notifications process

Every command accepts `--json`; `--debug` dumps modem traffic.

## Notes

- **GPS and the AT port.** On the EM160R, NMEA output routed to `/dev/gnss0`
  that nobody reads fills the modem's transmit queue and the AT port stops
  answering. `esimctl gps on` routes NMEA output nowhere and esimctl reads
  positions over AT instead. If the AT port ever stops answering, restart the
  modem with `esimctl modem off && esimctl modem on`.
- The kernel lets several processes open the AT port at once; esimctl
  serializes its own instances with a lock on the device.
- Enabling or disabling an eSIM profile refreshes the SIM; esimctl then
  delivers the operator notification.
- SM-DP+ servers use TLS certificates issued by the GSMA CI, which the system
  trust store doesn't know. esimctl then skips TLS verification: the server is
  still authenticated end to end by the eUICC, and the profile is encrypted
  for the eUICC.

## License

MIT
