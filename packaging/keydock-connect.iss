#define AppName "KeyDock Connect"
#define AppExe "KeyDockConnect.exe"
#define AppGuid "1F72CD21-C12E-40B7-9D93-98527EF65242"
#define InstallDir "{commonpf64}\KeyDock Connect"
#ifndef AppVersion
  #error "Run packaging/build-installer.ps1 to supply the release version."
#endif

[Setup]
AppId={{{#AppGuid}}
AppName={#AppName}
AppVersion={#AppVersion}
VersionInfoVersion={#AppVersion}
AppPublisher=Yungor
PrivilegesRequired=admin
ArchitecturesAllowed=x64os
ArchitecturesInstallIn64BitMode=x64os
DefaultDirName={#InstallDir}
UsePreviousAppDir=no
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
Name: "{commonprograms}\{#AppName}"; Filename: "{app}\{#AppExe}"
Name: "{commondesktop}\{#AppName}"; Filename: "{app}\{#AppExe}"; Tasks: desktopicon

[Code]
const
  UpdatePIDOption = '/KEYDOCKUPDATEPID=';
  SynchronizeProcess = $00100000;
  WaitObject0 = 0;
  ErrorInvalidParameter = 87;

function OpenProcess(DesiredAccess: DWORD; InheritHandle: BOOL; ProcessID: DWORD): THandle;
  external 'OpenProcess@kernel32.dll stdcall';
function WaitForSingleObject(Handle: THandle; Milliseconds: DWORD): DWORD;
  external 'WaitForSingleObject@kernel32.dll stdcall';
function CloseHandle(Handle: THandle): BOOL;
  external 'CloseHandle@kernel32.dll stdcall';

function UpdateParentPID: Integer;
var
  Index: Integer;
  Argument: String;
begin
  Result := 0;
  for Index := 1 to ParamCount do begin
    Argument := ParamStr(Index);
    if Pos(UpdatePIDOption, Uppercase(Argument)) = 1 then begin
      Result := StrToIntDef(Copy(Argument, Length(UpdatePIDOption) + 1, Length(Argument)), -1);
      Exit;
    end;
  end;
end;

function WaitForUpdatingApp: Boolean;
var
  ProcessID: Integer;
  ProcessHandle: THandle;
begin
  ProcessID := UpdateParentPID;
  Result := ProcessID = 0;
  if ProcessID <= 0 then
    Exit;
  ProcessHandle := OpenProcess(SynchronizeProcess, False, ProcessID);
  if ProcessHandle = 0 then begin
    // Windows reports an invalid process ID when the app has already exited.
    Result := DLLGetLastError = ErrorInvalidParameter;
  end else begin
    Result := WaitForSingleObject(ProcessHandle, 20000) = WaitObject0;
    CloseHandle(ProcessHandle);
  end;
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  ExitCode: Integer;
begin
  Result := '';
  if CompareText(RemoveBackslashUnlessRoot(WizardDirValue), ExpandConstant('{#InstallDir}')) <> 0 then begin
    Result := CustomMessage('FixedInstallDir');
    Exit;
  end;
  if not WaitForUpdatingApp then begin
    Result := CustomMessage('UpdateAppRunning');
    Exit;
  end;
  // Query the original user's registration. Never run an old uninstaller
  // from a user-writable directory under Setup's administrative token.
  if ExecAsOriginalUser(ExpandConstant('{sys}\reg.exe'),
    'query "HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\{{#AppGuid}}_is1"',
    ExpandConstant('{sys}'), SW_HIDE, ewWaitUntilTerminated, ExitCode) and (ExitCode = 0) then
    Result := CustomMessage('RemoveLegacyInstall');
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if (CurStep = ssDone) and (UpdateParentPID > 0) then
    SuppressibleMsgBox(CustomMessage('OnlineUpdateComplete'), mbInformation, MB_OK, IDOK);
end;

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

[Messages]
zh.ConfirmUninstall=要卸载 %1 吗？如果多个 Windows 账号使用过此程序，请先在各账号中恢复原 Codex 配置并退出助手。
en.ConfirmUninstall=Uninstall %1? If more than one Windows account used this program, restore the original Codex configuration and quit the app in each account first.
zh.FinishedLabel=KeyDock Connect 已安装。请从 Windows 开始菜单打开 KeyDock Connect。
zh.FinishedLabelNoIcons=KeyDock Connect 已安装。请从 Windows 开始菜单打开 KeyDock Connect。
en.FinishedLabel=KeyDock Connect is installed. Open it from the Windows Start menu.
en.FinishedLabelNoIcons=KeyDock Connect is installed. Open it from the Windows Start menu.

[CustomMessages]
zh.FixedInstallDir=请使用安装包默认的 Program Files 安装目录。
en.FixedInstallDir=Use the default Program Files installation directory.
zh.UpdateAppRunning=更新前请退出 KeyDock Connect，再重试安装。
en.UpdateAppRunning=Exit KeyDock Connect before retrying the update.
zh.OnlineUpdateComplete=KeyDock Connect 更新完成。请从 Windows 开始菜单重新打开。若从免安装版切换到安装版，请点一次“连接并测试”。
en.OnlineUpdateComplete=KeyDock Connect was updated. Open it again from the Windows Start menu. If you switched from the portable edition, click Connect and Test once.
zh.RemoveLegacyInstall=请退出旧版 KeyDock Connect，在 Windows 设置的“应用”中卸载它，然后重新运行此安装包。安装后请重新输入设备密钥。
en.RemoveLegacyInstall=Quit the old KeyDock Connect, uninstall it under Apps in Windows Settings, and run this installer again. Enter your Device Key again after installation.
zh.RestoreNeeded=无法安全恢复配置，已停止卸载。请在使用过 KeyDock Connect 的各 Windows 账号中恢复原 Codex 配置并退出助手，再由管理员登录自己的 Windows 账号重试卸载。如果程序文件缺失，请先重新安装。
en.RestoreNeeded=Uninstall stopped because settings could not be safely restored. In each Windows account that used KeyDock Connect, restore the original Codex configuration and quit the app. Then sign in to the administrator's Windows account and try uninstalling again. If any program files are missing, reinstall first.
