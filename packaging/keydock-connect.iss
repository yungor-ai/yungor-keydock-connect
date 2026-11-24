#define AppName "KeyDock Connect"
#define AppExe "KeyDockConnect.exe"
#ifndef AppVersion
  #error "Run packaging/build-installer.ps1 to supply the release version."
#endif

[Setup]
AppId={{1F72CD21-C12E-40B7-9D93-98527EF65242}
AppName={#AppName}
AppVersion={#AppVersion}
VersionInfoVersion={#AppVersion}
AppPublisher=Yungor
PrivilegesRequired=lowest
ArchitecturesAllowed=x64os
ArchitecturesInstallIn64BitMode=x64os
DefaultDirName={localappdata}\Programs\KeyDock Connect
DisableDirPage=yes
DisableProgramGroupPage=yes
UninstallDisplayIcon={app}\{#AppExe}
OutputDir=..\dist\installer
OutputBaseFilename=KeyDockConnect-Setup-windows-x64
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
CloseApplications=no

[Languages]
Name: "zh"; MessagesFile: "..\dist\inno-language\ChineseSimplified.isl"
Name: "en"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Files]
Source: "..\dist\app\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{userprograms}\{#AppName}"; Filename: "{app}\{#AppExe}"
Name: "{userdesktop}\{#AppName}"; Filename: "{app}\{#AppExe}"; Tasks: desktopicon

[Run]
Filename: "{app}\{#AppExe}"; Description: "{cm:LaunchProgram,{#AppName}}"; Flags: nowait postinstall skipifsilent

[Code]
// Run after the user confirms uninstall and before Inno removes any files.
// Abort removal if the executable cannot restore its managed external state.
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  ExitCode: Integer;
  Ready: Boolean;
begin
  if CurUninstallStep = usUninstall then begin
    CreateMutex('Local\Yungor.KeyDockConnect.Uninstalling');
    Ready := Exec(ExpandConstant('{app}\{#AppExe}'), '--prepare-uninstall',
      ExpandConstant('{app}'), SW_HIDE, ewWaitUntilTerminated, ExitCode);
    if (not Ready) or (ExitCode <> 0) then begin
      SuppressibleMsgBox(CustomMessage('RestoreNeeded'), mbError, MB_OK, IDOK);
      Abort;
    end;
  end;
end;

[CustomMessages]
zh.RestoreNeeded=无法安全恢复配置，已停止卸载。请在 KeyDock 接入助手中点击“恢复原 Codex 配置”，处理提示后点击“退出接入助手”，再重试卸载。如果程序文件缺失，请先重新安装到原位置。
en.RestoreNeeded=Uninstall stopped because settings could not be safely restored. Restore the Codex configuration in KeyDock Connect, resolve any reported conflict, quit the app, then retry. If the program is missing, reinstall to its original location first.
