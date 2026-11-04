# esimctl

A manager for the cellular modem of Linux laptops: eSIM profiles (a native
SGP.22 LPA), physical SIM and slot switching, PIN, mobile data with automatic
APN selection, and GPS. It comes with a terminal UI and an
[Omarchy](https://omarchy.org) bar widget.

Developed and tested on a ThinkPad X1 Carbon Gen 12 with a Quectel EM160R-GL,
on Iliad (Italy) with a physical SIM.

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
widget and the `esimctl-watch` user service, and (once, via pkexec):

- `contrib/70-esimctl.rules`, which gives the logged-in user the modem's AT
  and NMEA ports and tells ModemManager to leave the AT port to esimctl;
- `contrib/esimctl-modemmanager.conf`, a D-Bus policy that lets `wheel`
  users switch SIM slots (ModemManager allows only root by default; it still
  checks its polkit rule).

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
    esimctl data limit 10GB           # monthly cap; also: warn 80, day 15, action stop
    esimctl data usage                # used this period
    esimctl gps on && esimctl gps
    esimctl sms                       # list; also read, send, delete
    esimctl sms send +391234567890 "On my way"
    esimctl calls                     # list; hangup <id> rejects a call
    esimctl download 'LPA:1$smdp.example.com$MATCHING-ID'
    esimctl list | enable | disable | delete | nickname
    esimctl notifications process

Every command accepts `--json`; `--debug` dumps modem traffic.

## Messages and calls

`esimctl watch`, installed as the `esimctl-watch` user service, turns incoming
SMS into desktop notifications (with an Open action that shows the Messages
tab of the TUI) and incoming calls into a notification with a Reject action,
followed by a "Missed call" one. Laptop WWAN modules have no audio path to the
computer, so calls can be seen and rejected but not answered. ModemManager
doesn't track read state, so esimctl keeps it in
`~/.local/state/esimctl/sms-read.json`.

## Data limit

`esimctl watch` counts the traffic of the modem's interface every 30 seconds
(kernel counters, kept in `~/.local/state/esimctl/data-usage.json` across
restarts and counter resets). With a limit set it notifies at the warning
threshold (80% by default) and at the limit; with `action stop` it also turns
mobile data off until the period resets, and `esimctl data on` refuses to turn
it back on. Traffic from before the first count isn't known.

## Notes

- **GPS and the AT port.** On the EM160R, NMEA output routed to `/dev/gnss0`
  that nobody reads fills the modem's transmit queue and the AT port stops
  answering. `esimctl gps on` routes NMEA output nowhere and esimctl reads
  positions over AT instead. If the AT port ever stops answering, restart the
  modem with `esimctl modem off && esimctl modem on`.
- The modem powers one SIM slot at a time: eSIM profiles can only be managed
  while the eSIM slot is in use (`esimctl sim slot <n>`).
- ModemManager reports `sim-pin2` as "unlock required" on usable SIMs; PIN2
  only guards special SIM functions, so esimctl doesn't treat it as a lock.
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
