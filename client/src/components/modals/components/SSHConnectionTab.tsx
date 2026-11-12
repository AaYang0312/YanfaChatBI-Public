import React, { useState, useEffect } from 'react';
import { AlertCircle } from 'lucide-react';
import { Connection, SSHAuthMethod } from '../../../types/chat';
import { zhCN } from '../../../i18n/zh-CN';

// We'll define FormErrors type here since it's not exported from ConnectionModal
interface FormErrors {
  host?: string;
  port?: string;
  database?: string;
  username?: string;
  ssh_host?: string;
  ssh_port?: string;
  ssh_username?: string;
  ssh_private_key?: string;
  auth_database?: string;
}

interface SSHConnectionTabProps {
  formData: Connection;
  errors: FormErrors;
  touched: Record<string, boolean>;
  handleChange: (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => void;
  handleBlur: (e: React.FocusEvent<HTMLInputElement>) => void;
  validateField: (name: string, value: Connection) => string | undefined;
  mongoUriSshInputRef: React.RefObject<HTMLInputElement>;
  onMongoUriChange?: (uri: string) => void;
}

  // Build connection URI from form data
  const buildConnectionUri = (data: Connection): string => {
    if (!data.host || !data.database) return '';

    let uri = '';
    const username = data.username || '';
    // Always show <password> placeholder to indicate where password should go
    const password = '<password>';
    const host = data.host;
    const port = data.port || getDefaultPort(data.type);
    const database = data.database;

    switch (data.type) {
      case 'postgresql':
      case 'timescaledb':
      case 'yugabytedb':
        uri = `postgresql://${username}${username ? ':' + password : ''}@${host}${port ? ':' + port : ''}/${database}`;
        break;
      case 'mysql':
      case 'starrocks':
        uri = `mysql://${username}${username ? ':' + password : ''}@${host}${port ? ':' + port : ''}/${database}`;
        break;
      case 'clickhouse':
        uri = `clickhouse://${username}${username ? ':' + password : ''}@${host}${port ? ':' + port : ''}/${database}`;
        break;
      case 'mongodb':
        uri = `mongodb://${username}${username ? ':' + password : ''}@${host}${port ? ':' + port : ''}/${database}`;
        if (data.auth_database && data.auth_database !== 'admin') {
          uri += `?authSource=${data.auth_database}`;
        }
        break;
      default:
        return '';
    }

    return uri;
  };

  // Get default port for database type
  const getDefaultPort = (dbType: string): string => {
    switch (dbType) {
      case 'postgresql':
      case 'timescaledb':
      case 'yugabytedb':
        return '5432';
      case 'mysql':
      case 'starrocks':
        return '3306';
      case 'clickhouse':
        return '8123';
      case 'mongodb':
        return '27017';
      default:
        return '';
    }
  };
const SSHConnectionTab: React.FC<SSHConnectionTabProps> = ({
  formData,
  errors,
  touched,
  handleChange,
  handleBlur,
  validateField,
  mongoUriSshInputRef,
  onMongoUriChange
}) => {
  // State for SSH authentication method - sync with formData
  const [sshAuthMethod, setSshAuthMethod] = useState<SSHAuthMethod>(
    (formData.ssh_auth_method as SSHAuthMethod) || SSHAuthMethod.PublicKey
  );
  // State to track the connection URI value independently
  const [connectionUri, setConnectionUri] = useState<string>('');

  // Sync local state with formData when it changes
  useEffect(() => {
    if (formData.ssh_auth_method) {
      setSshAuthMethod(formData.ssh_auth_method as SSHAuthMethod);
    }
  }, [formData.ssh_auth_method]);

  // Handle auth method change and update formData
  const handleAuthMethodChange = (method: SSHAuthMethod) => {
    setSshAuthMethod(method);
    // Create synthetic event to update formData
    const syntheticEvent = {
      target: {
        name: 'ssh_auth_method',
        value: method
      }
    } as React.ChangeEvent<HTMLInputElement>;
    handleChange(syntheticEvent);
  };

  // Custom blur handler that validates the field using the passed validateField function
  const handleFieldBlur = (e: React.FocusEvent<HTMLInputElement>) => {
    const { name } = e.target;
    const error = validateField(name, formData);
    if (error) {
      // Error will be handled by the parent component via the validateField callback
      // The parent component updates the errors state
      console.log(`Validation error for ${name}: ${error}`);
    }
    // Call the parent's handleBlur to update touched state
    handleBlur(e);
  };

  // Get URI config based on database type
  const getUriConfig = (dbType: string): { label: string; placeholder: string; description: string } | null => {
    switch (dbType) {
      case 'postgresql':
      case 'timescaledb':
      case 'yugabytedb':
        return {
          label: 'PostgreSQL 连接 URI',
          placeholder: 'postgresql://username:password@host:port/database',
          description: '粘贴 PostgreSQL 连接串以自动填充下方字段'
        };
      case 'mysql':
      case 'starrocks':
        return {
          label: 'MySQL 连接 URI',
          placeholder: 'mysql://username:password@host:port/database',
          description: '粘贴 MySQL 连接串以自动填充下方字段'
        };
      case 'clickhouse':
        return {
          label: 'ClickHouse 连接 URI',
          placeholder: 'clickhouse://username:password@host:port/database',
          description: '粘贴 ClickHouse 连接串以自动填充下方字段'
        };
      case 'mongodb':
        return {
          label: 'MongoDB 连接 URI',
          placeholder: 'mongodb://username:password@host:port/database or mongodb+srv://username:password@host/database',
          description: '粘贴 MongoDB 连接串以自动填充下方字段'
        };
      default:
        return null;
    }
  };


  // Parse connection URI for different database types
  const parsePostgreSQLUri = (uri: string): Partial<Connection> => {
    try {
      const match = uri.match(/postgresql:\/\/(?:([^:@]+)(?::([^@]*))?@)?([^:/?]+)(?::(\d+))?\/([^?]+)(?:\?(.*))?/);
      if (match) {
        return {
          username: match[1] || '',
          password: match[2] || '',
          host: match[3],
          port: match[4] || '5432',
          database: match[5]
        };
      }
    } catch (e) {
      console.error('Error parsing PostgreSQL URI:', e);
    }
    return {};
  };

  const parseMySQLUri = (uri: string): Partial<Connection> => {
    try {
      const match = uri.match(/mysql:\/\/(?:([^:@]+)(?::([^@]*))?@)?([^:/?]+)(?::(\d+))?\/([^?]+)/);
      if (match) {
        return {
          username: match[1] || '',
          password: match[2] || '',
          host: match[3],
          port: match[4] || '3306',
          database: match[5]
        };
      }
    } catch (e) {
      console.error('Error parsing MySQL URI:', e);
    }
    return {};
  };

  const parseClickHouseUri = (uri: string): Partial<Connection> => {
    try {
      const match = uri.match(/clickhouse:\/\/(?:([^:@]+)(?::([^@]*))?@)?([^:/?]+)(?::(\d+))?\/([^?]+)/);
      if (match) {
        return {
          username: match[1] || '',
          password: match[2] || '',
          host: match[3],
          port: match[4] || '8123',
          database: match[5]
        };
      }
    } catch (e) {
      console.error('Error parsing ClickHouse URI:', e);
    }
    return {};
  };

  const parseMongoDBUri = (uri: string): Partial<Connection> => {
    try {
      const srvFormat = uri.startsWith('mongodb+srv://');
      const protocolMatch = uri.match(/^(mongodb(?:\+srv)?:\/\/)(.*)/);
      if (!protocolMatch) return {};

      const [, , remainder] = protocolMatch;
      const hasCredentials = remainder.includes('@');
      let username = '';
      let password = '';
      let hostPart = remainder;

      if (hasCredentials) {
        const lastAtIndex = remainder.lastIndexOf('@');
        const credentialsPart = remainder.substring(0, lastAtIndex);
        hostPart = remainder.substring(lastAtIndex + 1);

        const firstColonIndex = credentialsPart.indexOf(':');
        if (firstColonIndex !== -1) {
          username = decodeURIComponent(credentialsPart.substring(0, firstColonIndex));
          password = decodeURIComponent(credentialsPart.substring(firstColonIndex + 1));
        } else {
          username = decodeURIComponent(credentialsPart);
        }
      }

      let host = '';
      let port = srvFormat ? '27017' : '';
      let database = 'test';
      let authDatabase = 'admin';

      const pathIndex = hostPart.indexOf('/');
      if (pathIndex !== -1) {
        const hostPortPart = hostPart.substring(0, pathIndex);
        const pathPart = hostPart.substring(pathIndex + 1);

        const queryIndex = pathPart.indexOf('?');
        if (queryIndex !== -1) {
          database = pathPart.substring(0, queryIndex);
          const queryParams = new URLSearchParams(pathPart.substring(queryIndex + 1));
          if (queryParams.has('authSource')) {
            authDatabase = queryParams.get('authSource') || 'admin';
          }
        } else {
          database = pathPart;
        }

        const portIndex = hostPortPart.indexOf(':');
        if (portIndex !== -1) {
          host = hostPortPart.substring(0, portIndex);
          port = hostPortPart.substring(portIndex + 1);
        } else {
          host = hostPortPart;
        }
      } else {
        const portIndex = hostPart.indexOf(':');
        if (portIndex !== -1) {
          host = hostPart.substring(0, portIndex);
          port = hostPart.substring(portIndex + 1);
        } else {
          host = hostPart;
        }
      }

      return {
        username: username || '',
        password: password || '',
        host,
        port: port || (srvFormat ? '27017' : ''),
        database: database || 'test',
        auth_database: authDatabase
      };
    } catch (e) {
      console.error('Error parsing MongoDB URI:', e);
    }
    return {};
  };

  const parseConnectionUri = (uri: string, dbType: string) => {
    try {
      uri = uri.trim();
      if (!uri) return;

      let parsedData: Partial<Connection> = {};

      switch (dbType) {
        case 'postgresql':
        case 'timescaledb':
        case 'yugabytedb':
          parsedData = parsePostgreSQLUri(uri);
          break;
        case 'mysql':
        case 'starrocks':
          parsedData = parseMySQLUri(uri);
          break;
        case 'clickhouse':
          parsedData = parseClickHouseUri(uri);
          break;
        case 'mongodb':
          parsedData = parseMongoDBUri(uri);
          break;
        default:
          console.log(`URI parsing not supported for database type: ${dbType}`);
          return;
      }

      if (parsedData && Object.keys(parsedData).length > 0) {
        console.log(`${dbType} URI parsed successfully`, parsedData);

        Object.entries(parsedData).forEach(([key, value]) => {
          if (value !== undefined && value !== null) {
            const mockEvent = {
              target: { name: key, value: String(value) }
            } as React.ChangeEvent<HTMLInputElement>;
            handleChange(mockEvent);
          }
        });
      }
    } catch (err) {
      console.error(`Invalid ${dbType} URI format`, err);
    }
  };

  // Update connection URI when form data changes
  useEffect(() => {
    if (formData.host && formData.database) {
      setConnectionUri(buildConnectionUri(formData));
    } else {
      // Clear connection URI when type changes and no host/database are set
      setConnectionUri('');
    }
  }, [formData]);

  return (
    <>
      {/* SSH Tunnel Configuration */}
      <div className="mb-6 p-4 border-dashed border-2 border-gray-300 rounded-lg bg-gray-50">
            <h3 className="font-bold mb-3 text-md">{zhCN.connection.sshTitle}</h3>
            
            <div className="mb-4">
              <label className="block font-medium mb-2 text-sm">{zhCN.connection.sshHost}</label>
              <p className="text-gray-600 text-xs mb-1">SSH 服务器的主机名或 IP</p>
              <input
                type="text"
                name="ssh_host"
                value={formData.ssh_host || ''}
                onChange={handleChange}
                onBlur={handleFieldBlur}
                className={`neo-input w-full ${errors.ssh_host && touched.ssh_host ? 'border-red-500' : ''}`}
                placeholder="例如 ssh.example.com、192.168.1.10"
              />
              {errors.ssh_host && touched.ssh_host && (
                <p className="text-red-500 text-xs mt-1">{errors.ssh_host}</p>
              )}
            </div>
            
            <div className="mb-4">
              <label className="block font-medium mb-2 text-sm">{zhCN.connection.sshPort}</label>
              <p className="text-gray-600 text-xs mb-1">SSH 连接端口（通常为 22）</p>
              <input
                type="text"
                name="ssh_port"
                value={formData.ssh_port || '22'}
                onChange={handleChange}
                onBlur={handleFieldBlur}
                className={`neo-input w-full ${errors.ssh_port && touched.ssh_port ? 'border-red-500' : ''}`}
                placeholder="22"
              />
              {errors.ssh_port && touched.ssh_port && (
                <p className="text-red-500 text-xs mt-1">{errors.ssh_port}</p>
              )}
            </div>
            
            <div className="mb-6">
              <label className="block font-medium mb-2 text-sm">{zhCN.connection.sshUsername}</label>
              <p className="text-gray-600 text-xs mb-1">用于 SSH 认证的用户名</p>
              <input
                type="text"
                name="ssh_username"
                value={formData.ssh_username || ''}
                onChange={handleChange}
                onBlur={handleFieldBlur}
                className={`neo-input w-full ${errors.ssh_username && touched.ssh_username ? 'border-red-500' : ''}`}
                placeholder="例如 ubuntu、ec2-user"
              />
              {errors.ssh_username && touched.ssh_username && (
                <p className="text-red-500 text-xs mt-1">{errors.ssh_username}</p>
              )}
            </div>

            {/* SSH Authentication Method Selector */}
            <div className="mb-4 p-3 bg-blue-50 border border-blue-200 rounded-lg">
              <label className="block font-medium mb-2 text-sm">{zhCN.connection.sshAuthMethod}</label>
              <div className="flex gap-4">
                <label className="flex items-center gap-2 cursor-pointer">
                  <input
                    type="radio"
                    value={SSHAuthMethod.PublicKey}
                    checked={sshAuthMethod === SSHAuthMethod.PublicKey}
                    onChange={(e) => handleAuthMethodChange(e.target.value as SSHAuthMethod)}
                    className="w-4 h-4"
                  />
                  <span className="text-sm">{zhCN.connection.publicKey}</span>
                </label>
                <label className="flex items-center gap-2 cursor-pointer">
                  <input
                    type="radio"
                    value={SSHAuthMethod.Password}
                    checked={sshAuthMethod === SSHAuthMethod.Password}
                    onChange={(e) => handleAuthMethodChange(e.target.value as SSHAuthMethod)}
                    className="w-4 h-4"
                  />
                  <span className="text-sm">{zhCN.connection.password}</span>
                </label>
              </div>
            </div>

            {/* Public Key Authentication Fields */}
            {sshAuthMethod === SSHAuthMethod.PublicKey && (
              <>
                <div className="mb-4">
                  <label className="block font-medium mb-2 text-sm">{zhCN.connection.sshPrivateKey}</label>
                  <p className="text-gray-600 text-xs mb-1">粘贴 OpenSSH 私钥（格式：-----BEGIN OPENSSH PRIVATE KEY-----）</p>
                  {/* <p className="text-gray-500 text-xs mb-2 bg-gray-50 p-2 rounded border border-gray-200">
                    支持 OpenSSH 格式密钥。若为旧版 RSA 格式（-----BEGIN RSA PRIVATE KEY-----），请先转换。
                  </p> */}
                  <textarea
                    name="ssh_private_key"
                    value={formData.ssh_private_key || ''}
                    onChange={(e) => {
                      const mockEvent = {
                        target: {
                          name: 'ssh_private_key',
                          value: e.target.value
                        }
                      } as React.ChangeEvent<HTMLInputElement>;
                      handleChange(mockEvent);
                    }}
                    onBlur={() => {
                      const mockEvent = {
                        target: {
                          name: 'ssh_private_key'
                        }
                      } as React.FocusEvent<HTMLInputElement>;
                      handleBlur(mockEvent);
                      validateField('ssh_private_key', formData);
                    }}
                    className={`neo-input w-full font-mono text-xs ${errors.ssh_private_key && touched.ssh_private_key ? 'border-red-500' : ''}`}
                    placeholder="-----BEGIN OPENSSH PRIVATE KEY-----
...
...
-----END OPENSSH PRIVATE KEY-----"
                    rows={6}
                  />
                  {errors.ssh_private_key && touched.ssh_private_key && (
                    <p className="text-red-500 text-xs mt-1">{errors.ssh_private_key}</p>
                  )}
                </div>
                
                <div className="mb-4 p-3 bg-gray-50 border border-gray-200 rounded-lg">
                  <label className="block font-medium mb-2 text-sm">{zhCN.connection.sshKeyUrl}</label>
                  <p className="text-gray-600 text-xs mb-1">可提供安全 URL 以下载私钥</p>
                  <input
                    type="text"
                    name="ssh_private_key_url"
                    value={formData.ssh_private_key_url || ''}
                    onChange={handleChange}
                    className="neo-input w-full text-sm"
                    placeholder="例如 https://example.com/keys/private-key.pem"
                  />
                  {/* <p className="text-gray-500 text-xs mt-3">请仅使用完全信任的 URL 来源</p> */}
                </div>
                
                <div className="mb-4">
                  <label className="block font-medium mb-2 text-sm">{zhCN.connection.sshPassphrase}</label>
                  <p className="text-gray-600 text-xs mb-1">若私钥使用口令加密，请填写</p>
                  <input
                    type="password"
                    name="ssh_passphrase"
                    value={formData.ssh_passphrase || ''}
                    onChange={handleChange}
                    className="neo-input w-full text-base"
                    placeholder="若私钥没有口令可留空"
                  />
                </div>
              </>
            )}

            {/* Password Authentication Fields */}
            {sshAuthMethod === SSHAuthMethod.Password && (
              <div className="mb-4">
                <label className="block font-medium mb-2 text-sm">{zhCN.connection.sshPassword}</label>
                <p className="text-gray-600 text-xs mb-1">SSH 认证密码</p>
                <input
                  type="password"
                  name="ssh_password"
                  value={formData.ssh_password || ''}
                  onChange={handleChange}
                  className="neo-input w-full"
                  placeholder="请输入 SSH 密码"
                />
                {/* <p className="text-gray-500 text-xs mt-2 bg-yellow-50 p-2 rounded border border-yellow-200">
                  密码认证安全性低于公钥认证，建议尽量使用密钥。
                </p> */}
              </div>
            )}
          </div>

          {/* Database Settings Section */}
          <div>
            <h3 className="font-bold text-lg mb-3">{zhCN.connection.credentials}</h3>
            
            {/* Universal Connection URI Field - Show for supported database types */}
            {getUriConfig(formData.type) && formData.type !== 'spreadsheet' && (
              <div className="mb-6">
                <label className="block font-bold mb-2 text-lg">{getUriConfig(formData.type)!.label}</label>
                <p className="text-gray-600 text-sm mb-2">{getUriConfig(formData.type)!.description}</p>
                <input
                  type="text"
                  name="connection_uri"
                  ref={formData.type === 'mongodb' ? mongoUriSshInputRef : undefined}
                  className="neo-input w-full"
                  placeholder={getUriConfig(formData.type)!.placeholder}
                  value={connectionUri}
                  onChange={(e) => {
                    const uri = e.target.value;
                    // Update local state to allow free editing
                    setConnectionUri(uri);
                    // Save the URI value through the callback for MongoDB compatibility
                    if (formData.type === 'mongodb' && onMongoUriChange) {
                      onMongoUriChange(uri);
                    }
                    // Parse the URI based on the selected database type
                    parseConnectionUri(uri, formData.type);
                  }}
                />
                <p className="text-gray-500 text-xs mt-3">
                  {zhCN.connection.uriHint}
                  {formData.type === 'mongodb' && ' 支持标准与 Atlas SRV 格式。'}
                  {formData.type === 'postgresql' && ' 支持 sslmode 参数（例如 ?sslmode=require）。'}
                  {formData.type === 'timescaledb' && ' 支持 sslmode 参数（例如 ?sslmode=require）。'}
                  {formData.type === 'yugabytedb' && ' 支持 sslmode 参数（例如 ?sslmode=require）。'}
                  {formData.type === 'mysql' && ' 支持 useSSL 参数（例如 ?useSSL=true）。'}
                  {formData.type === 'starrocks' && ' 支持 useSSL 参数（例如 ?useSSL=true）。'}
                  {formData.type === 'clickhouse' && ' 支持 secure 参数（例如 ?secure=true）。'}
                </p>
              </div>
            )}

            {/* Divider between Connection URI and Manual Fields */}
            {getUriConfig(formData.type) && formData.type !== 'spreadsheet' && (
              <div className="flex items-center my-6">
                <div className="flex-1 border-t border-gray-300"></div>
                <span className="px-4 text-sm font-medium text-gray-500">{zhCN.common.or}</span>
                <div className="flex-1 border-t border-gray-300"></div>
              </div>
            )}
            
            {/* Host */}
            <div className="mb-6">
              <label className="block font-bold mb-2 text-lg">{zhCN.connection.host}</label>
              <p className="text-gray-600 text-sm mb-2">{zhCN.connection.hostHint}</p>
              <input
                type="text"
                name="host"
                value={formData.host}
                onChange={handleChange}
                onBlur={handleFieldBlur}
                className={`neo-input w-full ${errors.host && touched.host ? 'border-red-500' : ''}`}
                placeholder={zhCN.connection.hostPlaceholder}
              />
              {errors.host && touched.host && (
                <div className="flex items-center gap-1 mt-1 text-red-500 text-sm">
                  <AlertCircle className="w-4 h-4" />
                  <span>{errors.host}</span>
                </div>
              )}
            </div>
            
            {/* Port */}
            <div className="mb-6">
              <label className="block font-bold mb-2 text-lg">{zhCN.connection.port}</label>
              <p className="text-gray-600 text-sm mb-2">{zhCN.connection.portHint}</p>
              <input
                type="text"
                name="port"
                value={formData.port}
                onChange={handleChange}
                onBlur={handleFieldBlur}
                className={`neo-input w-full ${errors.port && touched.port ? 'border-red-500' : ''}`}
                placeholder={zhCN.connection.portPlaceholder}
              />
              {errors.port && touched.port && (
                <div className="flex items-center gap-1 mt-1 text-red-500 text-sm">
                  <AlertCircle className="w-4 h-4" />
                  <span>{errors.port}</span>
                </div>
              )}
            </div>
            
            {/* Database */}
            <div className="mb-6">
              <label className="block font-bold mb-2 text-lg">{zhCN.connection.database}</label>
              <p className="text-gray-600 text-sm mb-2">{zhCN.connection.databaseHint}</p>
              <input
                type="text"
                name="database"
                value={formData.database}
                onChange={handleChange}
                onBlur={handleFieldBlur}
                className={`neo-input w-full ${errors.database && touched.database ? 'border-red-500' : ''}`}
                placeholder={zhCN.connection.databasePlaceholder}
              />
              {errors.database && touched.database && (
                <div className="flex items-center gap-1 mt-1 text-red-500 text-sm">
                  <AlertCircle className="w-4 h-4" />
                  <span>{errors.database}</span>
                </div>
              )}
            </div>

            {/* MongoDB Authentication Database Field - Only show when MongoDB is selected */}
            {formData.type === 'mongodb' && (
              <div className="mb-6">
                <label className="block font-bold mb-2 text-lg">{zhCN.connection.authDatabase}</label>
                <p className="text-gray-600 text-sm mb-2">{zhCN.connection.authDatabaseHint}</p>
                <input
                  type="text"
                  name="auth_database"
                  value={formData.auth_database || 'admin'}
                  onChange={handleChange}
                  onBlur={handleFieldBlur}
                  className={`neo-input w-full ${errors.auth_database && touched.auth_database ? 'border-red-500' : ''}`}
                  placeholder="例如 admin"
                />
                {errors.auth_database && touched.auth_database && (
                  <div className="flex items-center gap-1 mt-1 text-red-500 text-sm">
                    <AlertCircle className="w-4 h-4" />
                    <span>{errors.auth_database}</span>
                  </div>
                )}
                <p className="text-gray-500 text-xs mt-2">
                  这是存储用户凭证的数据库。MongoDB Atlas 通常为 admin。
                </p>
              </div>
            )}
            
            {/* Username */}
            <div className="mb-6">
              <label className="block font-bold mb-2 text-lg">{zhCN.connection.username}</label>
              <p className="text-gray-600 text-sm mb-2">{zhCN.connection.usernameHint}</p>
              <input
                type="text"
                name="username"
                value={formData.username}
                onChange={handleChange}
                onBlur={handleFieldBlur}
                className={`neo-input w-full ${errors.username && touched.username ? 'border-red-500' : ''}`}
                placeholder="例如 db_user、assistant"
              />
              <p className="text-gray-500 text-sm mt-3">请仅赋予所需权限，例如只读、读写或有限访问。</p>
              {errors.username && touched.username && (
                <div className="flex items-center gap-1 mt-1 text-red-500 text-sm">
                  <AlertCircle className="w-4 h-4" />
                  <span>{errors.username}</span>
                </div>
              )}
            </div>
            
            {/* Password */}
            <div className="mb-6">
              <label className="block font-bold mb-2 text-lg">{zhCN.connection.password}</label>
              <p className="text-gray-600 text-sm mb-2">{zhCN.connection.passwordHint}</p>
              <input
                type="password"
                name="password"
                value={formData.password || ''}
                onChange={handleChange}
                className="neo-input w-full"
                placeholder={zhCN.connection.passwordPlaceholder}
              />
              <p className="text-gray-500 text-sm mt-3">若数据库无密码可留空，但建议为用户设置密码。</p>
            </div>

          </div>

    </>
  );
};

export default SSHConnectionTab; 