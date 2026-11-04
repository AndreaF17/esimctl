# esimctl

A native eSIM manager (SGP.22 LPA) for MBIM cellular modems on Linux, with a
terminal UI and an Omarchy bar widget. No lpac, ModemManager or libmbim needed:
it speaks MBIM directly to `/dev/wwan*mbim*` and implements the ES10 (eUICC)
and ES9+ (SM-DP+) interfaces itself.

Tested on a ThinkPad X1 Carbon Gen 12 with a Quectel EM160R-GL.

## Install

    ./install.sh

This builds `~/.local/bin/esimctl`, installs the `andrea.esim` bar widget, and
(once, via pkexec) the udev rule in `contrib/` that gives the logged-in user
access to the modem's MBIM port.

## Use

    esimctl tui                    # interactive; `s` scans a QR on screen
    esimctl list
    esimctl download 'LPA:1$smdp.example.com$MATCHING-ID'
    esimctl enable <iccid>         # disables the current profile
    esimctl nickname <iccid> Work
    esimctl delete <iccid>
    esimctl notifications process
    esimctl --json status          # used by the bar widget

Every command accepts `--json`; `--debug` dumps the MBIM traffic.

## Notes

- The kernel lets only one process use the MBIM port. If ModemManager is
  installed and running it owns the port, and esimctl reports it as busy.
- Enabling or disabling a profile triggers a SIM refresh; esimctl waits for
  the card to come back and then delivers the operator notification.
- If a client dies while holding the ISD-R channel, the modem won't let anyone
  else select it. esimctl detects this and resets the UICC to recover.
- SM-DP+ servers present TLS certificates issued by the GSMA CI, which the
  system trust store doesn't know. esimctl then skips TLS verification; the
  server is still authenticated end to end by the eUICC (AuthenticateServer)
  and the profile package is encrypted to the eUICC.
