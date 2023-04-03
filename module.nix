{ pkgs, config, lib, ... }:

with lib;

  let
    cfg = config.services.b58-mailer;
  in
  {
    options.services.b58-mailer = {

      enable = mkEnableOption "Base58 mailer service"; 

      mailerBin = mkOption {
        type = types.str;
        description = mdDoc "The package providing the b58-mailer binaries";
      };

      user = mkOption {
        type = types.str;
        default = "nobody";
        description = mdDoc "The user to run the b58-mailer binaries";
      };

      port = mkOption {
        type = types.port;
        default = 9090; 
        description = mdDoc "Port to start mailer on";
      };

      secretsFile = mkOption {
        type = with types; nullOr path;
        description = mdDoc "Name of file to load secrets from";
        default = "config.toml";
      };

      mailSendFrequency = mkOption {
        type = types.int;
        description = mdDoc "Frequency to check mailbox for new messages to send";
        default = 10;
      };

      dbFile = mkOption {
        type = types.str;
        description = mdDoc "Name of sqlite3 file to load";
        default = "mailer.sqlite";
      };

      mailGunDomain = mkOption {
        type = types.str;
        description = mdDoc "Domain name to send mailgun requests to";
      };
    };

    config = mkIf cfg.enable {
      systemd.services.b58-mailer = {
        description = "Base58 mailer service";
        after = [ "network.target" ];
        wantedBy = [ "multi-user.target" ];
	script = "PORT=${toString cfg.port} SECRETS_FILE=${toString cfg.secretsFile} MAIL_SEND_TIMER=${toString cfg.mailSendFrequency} DB_NAME=${toString cfg.dbFile} MAILGUN_DOMAIN=${toString cfg.mailGunDomain} ${cfg.mailerBin}";

        serviceConfig = {
          Type = "simple";
          User = "${cfg.user}";
        };
      };
    };	

  }

