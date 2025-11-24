#define MyAppName "mping"
#define MyAppVersion "1.0.2"
#define MyAppPublisher "oliverbenduhn"
#define MyAppURL "https://github.com/oliverbenduhn/MultiPingTUI"

[Setup]
AppId={{4E5F85B0-8B0E-4F50-9B8E-42CF5D4D3C72}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
AppSupportURL={#MyAppURL}
AppUpdatesURL={#MyAppURL}
DefaultDirName={pf}\{#MyAppName}
DefaultGroupName={#MyAppName}
ArchitecturesAllowed=x64
ArchitecturesInstallIn64BitMode=x64
LicenseFile=..\LICENSE
OutputBaseFilename=mping-setup
OutputDir=..\dist
UninstallDisplayIcon={app}\mping.exe
ChangesEnvironment=yes

[Files]
Source: "..\dist\mping-windows-amd64.exe"; DestDir: "{app}"; DestName: "mping.exe"; Flags: ignoreversion

[Tasks]
Name: "path"; Description: "Add mping to PATH"; GroupDescription: "Additional tasks:"; Flags: unchecked
Name: "desktopicon"; Description: "Create a desktop shortcut"; GroupDescription: "Additional tasks:"; Flags: unchecked

[Icons]
Name: "{group}\mping"; Filename: "{app}\mping.exe"
Name: "{commondesktop}\mping"; Filename: "{app}\mping.exe"; Tasks: desktopicon

[Registry]
Root: HKLM; Subkey: "SYSTEM\CurrentControlSet\Control\Session Manager\Environment"; ValueType: expandsz; ValueName: "Path"; ValueData: "{olddata};{app}"; Tasks: path; Check: NeedsAddPath(ExpandConstant('{app}'))

[Code]
function NeedsAddPath(Dir: string): Boolean;
var
  Paths: string;
begin
  if not RegQueryStringValue(HKLM, 'SYSTEM\CurrentControlSet\Control\Session Manager\Environment', 'Path', Paths) then
  begin
    Result := True;
    exit;
  end;
  Result := Pos(';' + LowerCase(Dir) + ';', ';' + LowerCase(Paths) + ';') = 0;
end;
