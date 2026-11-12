import { Connection, SSHAuthMethod } from '../types/chat';
import { zhCN } from '../i18n/zh-CN';

export interface FormErrors {
  host?: string;
  port?: string;
  database?: string;
  username?: string;
  ssl_cert_url?: string;
  ssl_key_url?: string;
  ssl_root_cert_url?: string;
  ssh_host?: string;
  ssh_port?: string;
  ssh_username?: string;
  ssh_private_key?: string;
}

export const isValidUrl = (url: string): boolean => {
  try {
    new URL(url);
    return true;
  } catch {
    return false;
  }
};

export const validateField = (name: string, value: Connection): string => {
  switch (name) {
    case 'host':
      if (!value.host.trim()) {
        return zhCN.connection.validation.hostRequired;
      }
      if (!/^[a-zA-Z0-9.-]+$/.test(value.host)) {
        return zhCN.connection.validation.hostInvalid;
      }
      break;
    case 'port':
      // For MongoDB, port is optional and can be empty
      if (value.type === 'mongodb') {
        return '';
      }
      if (!value.port) {
        return zhCN.connection.validation.portRequired;
      }
      // eslint-disable-next-line no-case-declarations
      const port = parseInt(value.port);
      if (isNaN(port) || port < 1 || port > 65535) {
        return zhCN.connection.validation.portRange;
      }
      break;
    case 'database':
      if (!value.database.trim()) {
        return zhCN.connection.validation.databaseRequired;
      }
      if (!/^[a-zA-Z0-9_-]+$/.test(value.database)) {
        return zhCN.connection.validation.databaseInvalid;
      }
      break;
    case 'username':
      if (!value.username.trim()) {
        return zhCN.connection.validation.usernameRequired;
      }
      break;
    case 'ssl_cert_url':
      if (value.use_ssl && value.ssl_mode !== 'disable' && value.ssl_mode !== 'require' && !value.ssl_cert_url?.trim()) {
        return zhCN.connection.validation.sslCertRequired;
      }
      if (value.ssl_cert_url && !isValidUrl(value.ssl_cert_url)) {
        return zhCN.connection.validation.urlInvalid;
      }
      break;
    case 'ssl_key_url':
      if (value.use_ssl && value.ssl_mode !== 'disable' && value.ssl_mode !== 'require' && !value.ssl_key_url?.trim()) {
        return zhCN.connection.validation.sslKeyRequired;
      }
      if (value.ssl_key_url && !isValidUrl(value.ssl_key_url)) {
        return zhCN.connection.validation.urlInvalid;
      }
      break;
    case 'ssl_root_cert_url':
      if (value.use_ssl && value.ssl_mode !== 'disable' && value.ssl_mode !== 'require' && !value.ssl_root_cert_url?.trim()) {
        return zhCN.connection.validation.sslRootRequired;
      }
      if (value.ssl_root_cert_url && !isValidUrl(value.ssl_root_cert_url)) {
        return zhCN.connection.validation.urlInvalid;
      }
      break;
    case 'ssh_host':
      if (value.ssh_enabled && !value.ssh_host?.trim()) {
        return zhCN.connection.validation.sshHostRequired;
      }
      if (value.ssh_host && !/^[a-zA-Z0-9.-]+$/.test(value.ssh_host)) {
        return zhCN.connection.validation.sshHostInvalid;
      }
      break;
    case 'ssh_port':
      if (value.ssh_enabled && !value.ssh_port) {
        return zhCN.connection.validation.sshPortRequired;
      }
      if (value.ssh_port) {
        const sshPort = parseInt(value.ssh_port);
        if (isNaN(sshPort) || sshPort < 1 || sshPort > 65535) {
          return zhCN.connection.validation.sshPortRange;
        }
      }
      break;
    case 'ssh_username':
      if (value.ssh_enabled && !value.ssh_username?.trim()) {
        return zhCN.connection.validation.sshUsernameRequired;
      }
      break;
    case 'ssh_private_key':
      if (value.ssh_enabled && value.ssh_auth_method === SSHAuthMethod.PublicKey && !value.ssh_private_key?.trim()) {
        return zhCN.connection.validation.sshKeyRequired;
      }
      break;
    default:
      return '';
  }
  return '';
};

export const formatConnectionString = (connection: Connection): string => {
  let result = `DATABASE_TYPE=${connection.type}
DATABASE_HOST=${connection.host}
DATABASE_PORT=${connection.port}
DATABASE_NAME=${connection.database}
DATABASE_USERNAME=${connection.username}
DATABASE_PASSWORD=`; // Mask password

  if (connection.use_ssl) {
    result += `\nUSE_SSL=true`;
    result += `\nSSL_MODE=${connection.ssl_mode || 'disable'}`;

    if (connection.ssl_cert_url) {
      result += `\nSSL_CERT_URL=${connection.ssl_cert_url}`;
    }

    if (connection.ssl_key_url) {
      result += `\nSSL_KEY_URL=${connection.ssl_key_url}`;
    }

    if (connection.ssl_root_cert_url) {
      result += `\nSSL_ROOT_CERT_URL=${connection.ssl_root_cert_url}`;
    }
  }

  if (connection.ssh_enabled) {
    result += `\nSSH_ENABLED=true`;
    result += `\nSSH_HOST=${connection.ssh_host || ''}`;
    result += `\nSSH_PORT=${connection.ssh_port || '22'}`;
    result += `\nSSH_USERNAME=${connection.ssh_username || ''}`;
    result += `\nSSH_PRIVATE_KEY=`; // Mask private key

    if (connection.ssh_passphrase) {
      result += `\nSSH_PASSPHRASE=`; // Mask passphrase
    }
  }

  return result;
};
