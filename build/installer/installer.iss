; Literature Translation 安装器（Inno Setup 6）。
; 由原版 package.bat 传入 AppVersion；PayloadDir 可选，默认读取原打包目录。
; 安装到用户可写目录（不弹 UAC），归档默认跟随 exe——程序与文献库自包含。

#define AppName "Literature Translation"
#ifndef AppVersion
#define AppVersion "1.2.4"
#endif
#ifndef PayloadDir
#define PayloadDir "..\package\Literature-Translation"
#endif

[Setup]
AppId={{8F4C2A6E-1D3B-4E7F-9A2C-5B6D8E1F3A90}}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} v{#AppVersion}
AppPublisher=Pet-Max
AppPublisherURL=https://github.com/Pet-Max/Literature-Translation
DefaultDirName={localappdata}\Programs\{#AppName}
PrivilegesRequired=lowest
DefaultGroupName={#AppName}
DisableProgramGroupPage=yes
OutputDir=..\bin
OutputBaseFilename=Literature-Translation-Setup-v{#AppVersion}-windows-amd64
SetupIconFile=..\windows\icon.ico
UninstallDisplayIcon={app}\Literature-Translation.exe
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
LicenseFile=..\..\LICENSE
CloseApplications=yes
DirExistsWarning=no

[Languages]
; 中文语言包随仓库分发（build\installer\ChineseSimplified.isl）——
; choco/winget 安装的 Inno 默认语言集不含中文，避免 CI 编译失败
Name: "chinesesimplified"; MessagesFile: "ChineseSimplified.isl"

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Files]
Source: "{#PayloadDir}\Literature-Translation.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#PayloadDir}\LICENSE"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\{#AppName}"; Filename: "{app}\Literature-Translation.exe"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\Literature-Translation.exe"; Tasks: desktopicon

[Run]
Filename: "{app}\Literature-Translation.exe"; Description: "{cm:LaunchProgram,{#AppName}}"; Flags: nowait postinstall skipifsilent
