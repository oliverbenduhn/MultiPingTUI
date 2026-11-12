# Desktop-GUI: Technik und Plattformkosten

Stand: 12. November 2026. Recherche, keine Framework- oder Plattformentscheidung. Ausgangscode: `ef04a40`. Kein GUI-Benchmark wurde durchgeführt.

## Ergebnis für die nächste Entscheidung

**Schlussfolgerung aus den Quellen:** Windows zuerst ist ein sinnvoller Lieferumfang, ohne die Architektur auf Windows festzulegen. Es gibt keinen belegten Grund, dass Windows-only an sich eine wesentlich schnellere oder bessere Monitoring-App erzeugt. Der sichere Gewinn ist weniger Installations-, Desktop- und Netzwerkvarianten testen zu müssen. Eine Windows-native WPF-Oberfläche bietet konkrete etablierte Windows-Steuerelemente und vermeidet WebView-Abhängigkeiten, kostet hier jedoch eine zweite Sprache und eine neue Prozessschnittstelle zum Go-Kern. Wails v3 und Fyne können den Go-Kern unmittelbar nutzen. Welche Oberfläche bei 4.000 Zielen besser ist, bleibt zu messen.

Empfohlene Entscheidungsvorlage: Wails v3 und Fyne als Go-nahe Kandidaten vergleichen; WPF plus Go-Prozess als bewusst Windows-spezifische Alternative. Windows zuerst veröffentlichen und Linux durch einen frühen technischen Versuch absichern; macOS erst nach bestätigtem Bedarf. Dies ist ein Vorschlag, keine aufgelöste menschliche Entscheidung.

## Nachgewiesene Fähigkeiten

| Option | Integration und Oberfläche | Tray und Tabelle | Wesentliche Kosten |
|---|---|---|---|
| Wails v3 | Go-Dienste mit generierten Frontend-Bindings, Weboberfläche im System-WebView. | Native Tray-API; Tabellenvirtualisierung muss das Frontend liefern. | Go plus Frontend-Build; Windows WebView2, Linux GTK/WebKit, plattformspezifische Tests. |
| Fyne | Go-Oberfläche; bestehender Kern kann nach Pakettrennung direkt angesprochen werden. | Desktop-Tray-API; `widget.Table` verwendet wiederverwendbare Zellvorlagen. | Keine JS-Schicht nötig; Tabelleninteraktionen und gewünschte Gestaltung konkret prototypisieren; CGo/Grafiktoolchain für Builds. |
| WPF mit Go-Kern als Kindprozess | Windows-UI in C#/XAML; vorgeschlagene Integration über versioniertes lokales IPC statt Portierung der Pinglogik. | DataGrid hat Zeilenvirtualisierung standardmäßig; WinForms `NotifyIcon` liefert Tray-Funktion. | Zweite Sprache/Toolchain, Prozesslebenszyklus und IPC; Linux erfordert später eine weitere Oberfläche. |

Wails v3 ist inzwischen stabil: Veröffentlichung v3.0.0 am 26.10.2026, v3.1.0 am 01.11.2026. Ältere Empfehlungen, die v3 ausschließlich als Alpha behandeln, sind überholt. v3.1 nennt experimentelle CGo-freie Backends; diese sind keine notwendige Grundlage dieses Vorschlags. Releaseversionen fest pinnen. Quellen: [v3-Veröffentlichung](https://wails.io/blog/wails-v3-released/), [v3.1-Release](https://github.com/wailsapp/wails/releases/tag/v3.1.0), [Bindings und Plattform-APIs](https://wails.io/reference/overview/).

Wails v2 bleibt eine Alternative, hat aber keinen eingebauten Tray-Runtime-Vertrag; für die ausdrücklich gewünschte Tray-App ist v3 daher der passendere Untersuchungskandidat. Quellen: [v2-Tray-Ticket](https://github.com/wailsapp/wails/issues/4990), [v3-Tray-API und Linux-Grenzen](https://wails.io/features/menus/systray/).

Fyne dokumentiert die Desktop-Tray-API und wiederverwendbare Tabellenzellen; das belegt Mechanismen, keine MultiPing-Leistungszahlen. Quellen: [Fyne Tray](https://docs.fyne.io/explore/systray/), [Fyne Table](https://docs.fyne.io/api/v2/widget/table/), [CGo und Crossbuilds](https://docs.fyne.io/started/cross-compiling/).

WPF bietet Datenbindung, Grafik und Controls; DataGrid erzeugt bei aktivierter Zeilenvirtualisierung nur benötigte Zeilen und recycelt sie. Der IPC-Prozess ist unser Integrationsvorschlag, keine vorhandene Projektfunktion. Quellen: [WPF](https://learn.microsoft.com/en-us/dotnet/desktop/wpf/overview/), [DataGrid-Virtualisierung](https://learn.microsoft.com/dotnet/api/system.windows.controls.datagrid.enablerowvirtualization), [NotifyIcon](https://learn.microsoft.com/en-us/dotnet/api/system.windows.forms.notifyicon).

## Plattformunterschiede und Netzwerkrechte

- **Windows:** Der aktuelle `pinger_probing.go` setzt pro-bing auf privileged. Der Name bedeutet laut upstream nicht zwingend Administratorrechte; Windows 10 wurde dort ohne Erhöhung getestet. Das ist keine Garantie für jede Endpoint-Policy. TCP nutzt im Projekt `net.Dialer`, Traceroute `tracert`. [pro-bing Betriebssystemhinweise](https://github.com/prometheus-community/pro-bing).
- **Linux:** Unprivilegierter ICMP hängt von `ping_group_range` ab; alternativ Raw-Socket-Berechtigung über `CAP_NET_RAW`. Normale Benutzer dürfen nicht pauschal die gesamte GUI als root betreiben müssen. Installer-/Fallback-Strategie bleibt zu entscheiden. Wails-Tray kann ohne passende Desktop-Erweiterung unsichtbar bleiben; Fensterschließen darf dann keinen unerreichbaren Hintergrundprozess hinterlassen. [ICMP-Voraussetzungen](https://github.com/prometheus-community/pro-bing), [Tray-Grenzen](https://wails.io/features/menus/systray/).
- **macOS:** Zusätzliche Signierungs-/Notarisierungskette und echte Gerätetests. ICMP-Verhalten muss mit der tatsächlich vendorten Version getestet werden. Die aktuelle GUI-Wahl beseitigt keine Netzwerkgrenzen. [Apple-Notarisierung](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution).

Lokale Primärquellen: `pinger_tcp.go` hat `!windows`, `pinger_tcp_win.go` nur `windows`. Unter macOS läuft daher der tcp-shaker-Pfad, dessen vendorte README für Nicht-Linux einen Dial-Fallback dokumentiert. Die Aussage in CLAUDE, macOS verwende `pinger_tcp_win.go`, stimmt nicht. Linux verwendet das SYN/SYN-ACK/RST-Verfahren; auf anderen Plattformen ist ein Verbindungsaufbau nötig. [tcp-shaker](https://github.com/tevino/tcp-shaker).

`traceroute.go` benötigt unter Windows `tracert`, sonst `traceroute` oder `tracepath` im PATH. `pinger_system.go` startet ein Betriebssystemprogramm. Installation ohne Terminal bedeutet daher auch, fehlende Hilfsprogramme im Paket oder durch verständliche GUI-Fehler zu behandeln. Keine GUI-Technik liefert diese Fähigkeiten automatisch nach.

## Installation und Updates

Wails verwendet System-WebViews und unterstützt plattformspezifische Pakete. Windows braucht WebView2; fehlende Laufzeit muss der Installer berücksichtigen. Linux benötigt eine definierte GTK/WebKit-Basis. Die aktuellen FAQ nennen GTK4/WebKitGTK 6.0 und einen zeitlich begrenzten GTK3-Pfad: für die gewählte Releaseversion prüfen, nicht aus älteren Anleitungen übernehmen. Entwicklungswerkzeuge werden auf Build-Maschinen benötigt, nicht beim Endnutzer. [Wails Installation](https://wails.io/quick-start/installation/), [Paketübersicht](https://wails.io/faq/).

Fyne liefert Desktop-Packaging; daraus folgen noch keine fertige Updatepolitik oder vollständig getesteten Installationspakete für dieses Projekt. WPF kann selbstenthaltend publiziert werden, wodurch eine separate .NET-Installation beim Nutzer entfällt. [Fyne Packaging](https://docs.fyne.io/started/packaging/), [.NET Deployment](https://learn.microsoft.com/en-us/dotnet/core/deploying/).

Für MSIX ist eine vertrauenswürdige Signatur nötig; ein EXE-Installer ist eine separate Paketentscheidung. macOS-Verteilung mit Developer ID erfordert die passende Signierung und Notarisierung. Wails bietet Signierungswerkzeuge; diese ersetzen keine Identitäten und Zertifikate. [Microsoft MSIX](https://learn.microsoft.com/en-us/windows/msix/package/signing-package-overview), [Apple](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution), [Wails Signing](https://wails.io/guides/build/signing/).

Wails v3 hat einen Updater mit Providern und Signaturprüfung; Installerformate wie MSI/DMG sind nicht direkt dessen Update-Artefakte. Der bestehende CLI-Selfupdater darf daher nicht ungeprüft für GUI-Pakete übernommen werden. Paketverwaltung und In-App-Updates müssen eine gemeinsame Zuständigkeit erhalten. [Updater](https://wails.io/guides/updater/).

## 4.000 Ziele: offen zu validieren

Alle drei Kandidaten haben einen geeigneten Mechanismus zur Begrenzung gerenderter Zeilen. Ein Webfrontend kann beispielsweise TanStack Virtual verwenden; das ist eine zusätzliche, zu begründende Abhängigkeit, keine getroffene Wahl. [Virtualisierung](https://tanstack.com/virtual/latest/docs/introduction).

Keiner der Quellen belegt 4.000 aktive MultiPing-Ziele mit den gewünschten Aktualisierungsraten. Messplan vor Zusage: 4.000 deterministische simulierte Ziele, Scrollen/Filtern/Sortieren bei 100 ms und 1 s; UI-Latenz, CPU, Speicher und Bridge-Datenvolumen; anschließend kontrollierte Backend-Lasttests. System-Ping erzeugt je Ziel Prozesse und ist gesondert zu bewerten. Eingeklappte oder versteckte Fenster sollen weniger UI-Arbeit auslösen, während die Überwachung weiterläuft. Dies sind Prüfvorschläge, keine gemessenen Eigenschaften.

Die derzeitigen Kern-Dateien sind `package main`. Direkte Wiederverwendung durch einen separaten GUI-Einstieg benötigt eine bewusste Paketgrenze. Ein WPF-Kindprozess benötigt stattdessen einen definierten IPC-Vertrag; der vorhandene HTTP-Server allein ist noch kein vollständiger GUI-Vertrag. Diese Architekturentscheidung gehört in das separate Kern-Ticket.
