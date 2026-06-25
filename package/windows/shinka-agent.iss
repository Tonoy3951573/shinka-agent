; ── Shinka Dynamics Local Agent — Inno Setup Script ──────────────────────────
; This script compiles the agent executable and static FFmpeg into a single 
; Windows Installer (.exe) with a setup wizard that prompts for connection credentials.
;
; Requirements to compile:
;   1. Download Inno Setup (https://jrsoftware.org/isinfo.php)
;   2. Compile the agent for Windows: GOOS=windows GOARCH=amd64 go build -o shinka-agent.exe
;   3. Place a static build of 'ffmpeg.exe' in the agent directory.
; ──────────────────────────────────────────────────────────────────────────────

[Setup]
AppName=Shinka Dynamics Local Agent
AppVersion=1.0.0
DefaultDirName={pf}\Shinka Dynamics\Agent
DefaultGroupName=Shinka Dynamics
UninstallDisplayIcon={app}\shinka-agent.exe
Compression=lzma2
SolidCompression=yes
OutputDir=..\..\dist-installers
OutputBaseFilename=shinka-agent-setup
PrivilegesRequired=admin

[Files]
Source: "..\shinka-agent.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\ffmpeg.exe"; DestDir: "{app}"; Flags: ignoreversion

[Dirs]
Name: "{app}\clips"

[Code]
var
  ServerPage: TInputQueryWizardPage;

procedure InitializeWizard;
begin
  // Create a custom wizard page to collect server details
  ServerPage := CreateInputQueryPage(wpWelcome,
    'Server Connection Details', 'Configure connection to Shinka Dynamics Cloud',
    'Please enter the dashboard server URL and the API key generated for this agent.');
  
  ServerPage.Add('Server URL (e.g. http://your-cloud-ip:3000):', False);
  ServerPage.Add('Agent API Key (starts with sk_):', True);
  ServerPage.Add('Agent Name (e.g. Front Counter):', False);

  // Set default placeholder/fallback values
  ServerPage.Values[0] := 'http://localhost:3000';
  ServerPage.Values[1] := '';
  ServerPage.Values[2] := 'Windows Agent';
end;

function NextButtonClick(CurPageID: Integer): Boolean;
begin
  Result := True;
  if CurPageID = ServerPage.ID then begin
    if (ServerPage.Values[0] = '') or (ServerPage.Values[1] = '') then begin
      MsgBox('You must enter both the Server URL and the Agent API Key to proceed.', mbError, MB_OK);
      Result := False;
    end;
  end;
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  ConfigLines: TArrayOfString;
  ConfigPath: String;
  ResultCode: Integer;
begin
  if CurStep = ssPostInstall then begin
    // 1. Generate the agent.yml config file using collected user inputs
    ConfigPath := ExpandConstant('{app}\agent.yml');
    SetArrayLength(ConfigLines, 3);
    ConfigLines[0] := 'server_url: "' + ServerPage.Values[0] + '"';
    ConfigLines[1] := 'api_key: "' + ServerPage.Values[1] + '"';
    ConfigLines[2] := 'agent_name: "' + ServerPage.Values[2] + '"';
    
    if not SaveStringsToFile(ConfigPath, ConfigLines, False) then begin
      MsgBox('Failed to write configuration file. Installation aborted.', mbError, MB_OK);
      Abort;
    end;

    // Restrict permissions on agent.yml config file
    if not Exec(ExpandConstant('{sys}\icacls.exe'),
      '"' + ConfigPath + '" /inheritance:r /grant:r *S-1-5-32-544:F /grant:r *S-1-5-18:F',
      '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then begin
      RaiseException('Failed to execute icacls.exe to restrict configuration file permissions.');
    end;
    if ResultCode <> 0 then begin
      RaiseException('Failed to restrict configuration file permissions. Exit code: ' + IntToStr(ResultCode));
    end;

    // Add ffmpeg to local path environment for this execution session if needed
    // 2. Install the agent as a persistent Windows Service
    // We run sc.exe to register the service
    if not Exec(ExpandConstant('{sys}\sc.exe'),
      'create ShinkaAgent start= auto binPath= "' + ExpandConstant('{app}\shinka-agent.exe') + ' -config ' + ConfigPath + '"',
      '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then begin
      RaiseException('Failed to execute sc.exe to create service.');
    end;
    if ResultCode <> 0 then begin
      RaiseException('Failed to create the ShinkaAgent service. Exit code: ' + IntToStr(ResultCode));
    end;
      
    if not Exec(ExpandConstant('{sys}\sc.exe'),
      'description ShinkaAgent "Shinka Dynamics Local Camera Stream Ingestion Service"',
      '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then begin
      RaiseException('Failed to execute sc.exe to set service description.');
    end;
    if ResultCode <> 0 then begin
      RaiseException('Failed to set ShinkaAgent service description. Exit code: ' + IntToStr(ResultCode));
    end;

    // 3. Start the service immediately
    if not Exec(ExpandConstant('{sys}\sc.exe'), 'start ShinkaAgent', '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then begin
      RaiseException('Failed to execute sc.exe to start service.');
    end;
    if ResultCode <> 0 then begin
      RaiseException('Failed to start the ShinkaAgent service. Exit code: ' + IntToStr(ResultCode));
    end;
  end;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  ResultCode: Integer;
begin
  if CurUninstallStep = usUninstall then begin
    // Stop and delete the Windows service before files are removed
    Exec(ExpandConstant('{sys}\sc.exe'), 'stop ShinkaAgent', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
    Exec(ExpandConstant('{sys}\sc.exe'), 'delete ShinkaAgent', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  end;
end;
