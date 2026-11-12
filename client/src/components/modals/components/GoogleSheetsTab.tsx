import React, { useState, useEffect } from 'react';
import { AlertCircle, CheckCircle, Loader2, ExternalLink, RefreshCw, Info } from 'lucide-react';
import { Connection } from '../../../types/chat';
import googleService from '../../../services/googleService';
import { formatServiceError, zhCN } from '../../../i18n/zh-CN';

interface GoogleSheetsTabProps {
  formData: Connection;
  handleChange: (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => void;
  isEditMode: boolean; // true if editing existing connection, false if creating new
  onGoogleAuthChange: (authData: { 
    google_sheet_id: string; 
    google_sheet_url: string;
    google_auth_token: string; 
    google_refresh_token: string;
  }) => void;
  onRefreshData?: () => void; // Callback to trigger refresh knowledge modal
}

const GoogleSheetsTab: React.FC<GoogleSheetsTabProps> = ({
  formData,
  handleChange,
  isEditMode,
  onGoogleAuthChange,
  onRefreshData,
}) => {
  const [isAuthenticating, setIsAuthenticating] = useState(false);
  const [authError, setAuthError] = useState<string | null>(null);
  const [authSuccess, setAuthSuccess] = useState(false);
  const [sheetUrl, setSheetUrl] = useState('');
  const [sheetInfo, setSheetInfo] = useState<{
    title: string;
    sheet_count: number;
    sheets?: string[];
  } | null>(null);
  const [isValidating, setIsValidating] = useState(false);
  const [userEmail, setUserEmail] = useState<string | null>(null);

  // Load existing URL if available
  useEffect(() => {
    if (formData.google_sheet_url) {
      setSheetUrl(formData.google_sheet_url);
    }
  }, [formData.google_sheet_url]);

  // Helper function to check if token is expired
  const isTokenExpired = (): boolean => {
    const expiry = localStorage.getItem('google_token_expiry');
    if (!expiry) return true;
    
    const expiryTime = new Date(expiry).getTime();
    const now = new Date().getTime();
    // Consider token expired if it expires in less than 5 minutes
    return now > (expiryTime - 5 * 60 * 1000);
  };

  // Helper function to refresh token
  const refreshToken = async (): Promise<boolean> => {
    const refresh = localStorage.getItem('google_refresh_token');
    if (!refresh) return false;
    
    try {
      const data = await googleService.refreshGoogleToken(refresh);
      localStorage.setItem('google_access_token', data.access_token);
      if (data.refresh_token) {
        localStorage.setItem('google_refresh_token', data.refresh_token);
      }
      if (data.expiry) {
        localStorage.setItem('google_token_expiry', data.expiry);
      }
      return true;
    } catch (error) {
      console.error('Failed to refresh token:', error);
      // Clear tokens if refresh fails
      localStorage.removeItem('google_access_token');
      localStorage.removeItem('google_refresh_token');
      localStorage.removeItem('google_user_email');
      localStorage.removeItem('google_token_expiry');
      setAuthSuccess(false);
      setUserEmail(null);
      return false;
    }
  };

  // Check for existing tokens on mount
  useEffect(() => {
    const checkAuth = async () => {
      const accessToken = localStorage.getItem('google_access_token');
      const refresh = localStorage.getItem('google_refresh_token');
      const email = localStorage.getItem('google_user_email');
      
      if (accessToken && refresh && email) {
        // Check if token is expired
        if (isTokenExpired()) {
          // Try to refresh
          const refreshed = await refreshToken();
          if (refreshed) {
            setAuthSuccess(true);
            setUserEmail(email);
          }
        } else {
          setAuthSuccess(true);
          setUserEmail(email);
        }
      }
    };
    
    checkAuth();
  }, []); // Only run on mount

  // Extract sheet ID from URL
  const extractSheetId = (url: string): string | null => {
    const patterns = [
      /\/spreadsheets\/d\/([a-zA-Z0-9-_]+)/,
      /[?&]id=([a-zA-Z0-9-_]+)/,
      /^([a-zA-Z0-9-_]+)$/
    ];
    
    for (const pattern of patterns) {
      const match = url.match(pattern);
      if (match) {
        return match[1];
      }
    }
    
    return null;
  };

  // Build Google OAuth URL for spreadsheet access
  const buildGoogleOAuthURL = (): string => {
    const GOOGLE_CLIENT_ID = import.meta.env.VITE_GOOGLE_CLIENT_ID;
    const GOOGLE_REDIRECT_URI = import.meta.env.VITE_GOOGLE_REDIRECT_URI || 'http://localhost:5173/auth/google/callback';
    
    if (!GOOGLE_CLIENT_ID) {
      throw new Error('Google Client ID 未配置');
    }

    const state = btoa(JSON.stringify({ purpose: 'spreadsheet' }));
    const scope = 'https://www.googleapis.com/auth/spreadsheets https://www.googleapis.com/auth/drive.readonly https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile';

    const authUrl = new URL('https://accounts.google.com/o/oauth2/v2/auth');
    authUrl.searchParams.append('client_id', GOOGLE_CLIENT_ID);
    authUrl.searchParams.append('redirect_uri', GOOGLE_REDIRECT_URI);
    authUrl.searchParams.append('response_type', 'code');
    authUrl.searchParams.append('scope', scope);
    authUrl.searchParams.append('access_type', 'offline');
    authUrl.searchParams.append('state', state);
    authUrl.searchParams.append('prompt', 'consent');

    return authUrl.toString();
  };

  // Handle Google authentication for spreadsheet access
  const handleGoogleAuth = async () => {
    try {
      setIsAuthenticating(true);
      setAuthError(null);
      
      // Build OAuth URL
      const oauthUrl = buildGoogleOAuthURL();
      
      // Open a popup window for the OAuth flow
      const width = 600;
      const height = 600;
      const left = window.screenX + (window.outerWidth - width) / 2;
      const top = window.screenY + (window.outerHeight - height) / 2;
      
      const authWindow = window.open(
        oauthUrl, 
        'google-sheets-auth', 
        `width=${width},height=${height},left=${left},top=${top}`
      );
      
      if (!authWindow) {
        throw new Error('无法打开认证窗口');
      }
      
      // Set up message listener before initiating OAuth
      const handleMessage = async (event: MessageEvent) => {
        // Verify origin for security
        if (event.origin !== window.location.origin) return;
        
        if (event.data.type === 'google-auth-success') {
          // The spreadsheet OAuth returns the authorization code
          const { code } = event.data;
          
          try {
            // Exchange the code for tokens using the backend endpoint
            const redirectURI = import.meta.env.VITE_GOOGLE_REDIRECT_URI || 'http://localhost:5173/auth/google/callback';
            
            const tokenData = await googleService.exchangeAuthCode(code, redirectURI);
            
            // Extract email from user object
            const userEmail = tokenData.user?.email || tokenData.user_email;
            if (!userEmail) {
              console.warn('No email found in token response. Token data keys:', Object.keys(tokenData));
              throw new Error('Google 认证未返回邮箱');
            }
            
            // Store tokens persistently
            localStorage.setItem('google_access_token', tokenData.access_token);
            localStorage.setItem('google_refresh_token', tokenData.refresh_token);
            localStorage.setItem('google_user_email', userEmail);
            if (tokenData.expiry) {
              localStorage.setItem('google_token_expiry', tokenData.expiry);
            }
            
            setUserEmail(userEmail);
            setAuthSuccess(true);
            setIsAuthenticating(false);
            
            // Close the auth window
            if (authWindow) {
              authWindow.close();
            }
            
            window.removeEventListener('message', handleMessage);
          } catch (exchangeError: unknown) {
            setAuthError(formatServiceError(exchangeError).summary);
            setIsAuthenticating(false);
            if (authWindow) {
              authWindow.close();
            }
            window.removeEventListener('message', handleMessage);
          }
        } else if (event.data.type === 'google-auth-error') {
          setAuthError(event.data.error ? formatServiceError(event.data.error).summary : '认证失败');
          setIsAuthenticating(false);
          
          if (authWindow) {
            authWindow.close();
          }
          
          window.removeEventListener('message', handleMessage);
        }
      };
      
      window.addEventListener('message', handleMessage);
      
      // Check if window was closed without auth
      const checkClosed = setInterval(() => {
        if (authWindow && authWindow.closed) {
          clearInterval(checkClosed);
          setIsAuthenticating(false);
          window.removeEventListener('message', handleMessage);
        }
      }, 1000);
      
    } catch (error: unknown) {
      const errorMessage = formatServiceError(error).summary;
      setAuthError(errorMessage);
      setIsAuthenticating(false);
    }
  };

  // Validate Google Sheet access
  const validateSheetAccess = async () => {
    // Check if token needs refresh
    if (isTokenExpired()) {
      const refreshed = await refreshToken();
      if (!refreshed) {
        setAuthError('认证已过期，请重新登录。');
        return;
      }
    }
    
    const sheetId = extractSheetId(sheetUrl);
    if (!sheetId) {
      setAuthError('无效的 Google Sheets URL');
      return;
    }
    
    const accessToken = localStorage.getItem('google_access_token');
    const googleRefreshToken = localStorage.getItem('google_refresh_token');
    
    if (!accessToken || !googleRefreshToken) {
      setAuthError('请先完成 Google 认证');
      return;
    }
    
    try {
      setIsValidating(true);
      setAuthError(null);
      
      const data = await googleService.validateSheet(accessToken, googleRefreshToken, sheetId);
      setSheetInfo(data);
      
      // Update form data with Google Sheets info
      onGoogleAuthChange({
        google_sheet_id: sheetId,
        google_sheet_url: sheetUrl,
        google_auth_token: accessToken,
        google_refresh_token: googleRefreshToken
      });
      
      // Set database name based on sheet title
      const dbName = data.title.toLowerCase().replace(/[^a-z0-9]/g, '_').substring(0, 50);
      const event = {
        target: {
          name: 'database',
          value: dbName
        }
      } as React.ChangeEvent<HTMLInputElement>;
      handleChange(event);
      
    } catch (error: unknown) {
      const errorMessage = formatServiceError(error).summary;
      setAuthError(errorMessage);
      setSheetInfo(null);
    } finally {
      setIsValidating(false);
    }
  };

  // Handle sheet URL change
  const handleSheetUrlChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    setSheetUrl(e.target.value);
    setSheetInfo(null);
  };

  // Re-authenticate if needed
  const handleReAuth = () => {
    setAuthSuccess(false);
    setUserEmail(null);
    setSheetInfo(null);
    localStorage.removeItem('google_access_token');
    localStorage.removeItem('google_refresh_token');
    localStorage.removeItem('google_user_email');
    localStorage.removeItem('google_token_expiry');
    handleGoogleAuth();
  };

  return (
    <div className="space-y-6">
      {/* Instructions */}
      <div className="p-4 bg-blue-50 border-2 border-blue-200 rounded-lg">
        <div className="flex items-start gap-2">
          <Info className="w-5 h-5 text-blue-600 mt-0.5 flex-shrink-0" />
          <div className="text-sm text-blue-800">
            <p className="font-semibold mb-2 text-base">{zhCN.sheets.howTo}</p>
            <ol className="list-decimal ml-4 space-y-1">
              <li>点击“使用 Google 认证”登录 Google 账号</li>
              <li><strong>授予只读</strong>访问权限</li>
              <li>粘贴 Google Sheets URL</li>
              <li>点击“连接”以连接该表格</li>
            </ol>
          </div>
        </div>
      </div>

      {/* Google Authentication */}
      <div>
        <label className="block font-bold mb-2 text-lg">{zhCN.sheets.authTitle}</label>
        <p className="text-gray-600 text-sm mb-4">
          {zhCN.sheets.authHint}
        </p>
        
        {!authSuccess ? (
          <button
            type="button"
            onClick={handleGoogleAuth}
            disabled={isAuthenticating}
            className="neo-button-secondary w-full flex items-center justify-center gap-2"
          >
            {isAuthenticating ? (
              <>
                <Loader2 className="w-5 h-5 animate-spin" />
                <span>{zhCN.sheets.authenticating}</span>
              </>
            ) : (
              <>
                <img src="https://www.google.com/favicon.ico" alt="Google" className="w-5 h-5" />
                <span>{zhCN.sheets.authenticate}</span>
              </>
            )}
          </button>
        ) : (
          <div className="p-4 bg-green-50 border-2 border-green-200 rounded-lg">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-3">
                <CheckCircle className="w-5 h-5 text-green-600" />
                <div>
                  <p className="font-medium text-green-800">{zhCN.sheets.authenticated}</p>
                  {userEmail && (
                    <p className="text-sm text-green-600">{userEmail}</p>
                  )}
                </div>
              </div>
              <button
                type="button"
                onClick={handleReAuth}
                className="px-3 py-2 text-sm text-black hover:bg-green-100 hover:text-green-800 rounded-lg transition-colors border border-black hover:border-green-400"
                title={zhCN.sheets.switch}
              >
                <div className="flex items-center gap-2">
                  <RefreshCw className="w-4 h-4" />
                  <span className="hidden sm:inline">{zhCN.sheets.switch}</span>
                </div>
              </button>
            </div>
          </div>
        )}
      </div>

      {/* Google Sheets URL */}
      {authSuccess && (
        <div>
          <label className="block font-bold mb-2 text-lg">{zhCN.sheets.url}</label>
          <p className="text-gray-600 text-sm mb-4">
            {isEditMode && formData.google_sheet_url
              ? '已连接的 Google Sheets URL（只读）'
              : '粘贴要连接的 Google Sheets URL'
            }
          </p>
          
          <div className="flex gap-2">
            <input
              type="text"
              value={sheetUrl}
              onChange={handleSheetUrlChange}
              placeholder="https://docs.google.com/spreadsheets/d/..."
              className={`neo-input flex-1 ${isEditMode && formData.google_sheet_url ? 'bg-gray-100 text-gray-600' : ''}`}
              disabled={isValidating || (isEditMode && Boolean(formData.google_sheet_url))}
              title={isEditMode && formData.google_sheet_url ? zhCN.sheets.urlLocked : ""}
            />
            {isEditMode && formData.google_sheet_url ? (
              <button
                type="button"
                onClick={onRefreshData}
                disabled={!onRefreshData}
                className="neo-button-secondary flex items-center gap-2"
                title={zhCN.sheets.refreshData}
              >
                <RefreshCw className="w-4 h-4" />
                {zhCN.sheets.refreshData}
              </button>
            ) : (
              <button
                type="button"
                onClick={validateSheetAccess}
                disabled={!sheetUrl || isValidating}
                className="neo-button-secondary"
                title={zhCN.sheets.connect}
              >
                {isValidating ? (
                  <Loader2 className="w-4 h-4 animate-spin" />
                ) : (
                  zhCN.sheets.connect
                )}
              </button>
            )}
          </div>
          
          {sheetUrl && (
            <a
              href={sheetUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="mt-3 inline-flex items-center gap-1 text-sm text-blue-600 hover:text-blue-800"
            >
              {zhCN.sheets.open}
              <ExternalLink className="w-3 h-3" />
            </a>
          )}
        </div>
      )}

      {/* Sheet Information */}
      {sheetInfo && (
        <div className="p-4 bg-gray-50 border-2 border-gray-200 rounded-lg">
          <h4 className="font-bold mb-3">{zhCN.sheets.info}</h4>
          <div className="space-y-2 text-sm">
            <div className="flex gap-1">
              <span className="text-gray-600">{zhCN.sheets.sheetName}</span>
              <span className="font-medium">{sheetInfo.title}</span>
            </div>
            <div className="flex gap-1">
              <span className="text-gray-600">{zhCN.sheets.pages}</span>
              <span className="font-medium">{sheetInfo.sheet_count}</span>
            </div>
            {sheetInfo.sheets && sheetInfo.sheets.length > 0 && (
              <div>
                <span className="text-gray-600">{zhCN.sheets.pageList}</span>
                <ul className="mt-1 ml-4 list-disc">
                  {sheetInfo.sheets.map((sheet: string, index: number) => (
                    <li key={index} className="text-gray-800">{sheet}</li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        </div>
      )}

      {/* Hidden fields for connection */}
      <input type="hidden" name="host" value="google-sheets" onChange={handleChange} />
      <input type="hidden" name="port" value="0" onChange={handleChange} />
      <input type="hidden" name="username" value="google-user" onChange={handleChange} />
      <input type="hidden" name="password" value="oauth" onChange={handleChange} />

      {/* Error message */}
      {authError && (
        <div className="p-3 bg-red-50 border-2 border-red-200 rounded-lg">
          <div className="flex items-center gap-2 text-red-600">
            <AlertCircle className="w-4 h-4" />
            <p className="text-sm">{authError}</p>
          </div>
        </div>
      )}

      {/* Data Privacy Notice */}
      <div className="p-4 bg-gray-100 border border-gray-300 rounded-lg">
        <div className="flex items-start gap-2">
          <AlertCircle className="w-5 h-5 text-gray-600 mt-0.5 flex-shrink-0" />
          <div className="text-sm text-gray-700">
            <p className="font-semibold text-base mb-1">{zhCN.files.security}</p>
            <p>
              Google Sheets 数据将同步并加密存储。我们仅申请只读访问。认证令牌已加密，你可以随时在 Google 账号设置中撤销访问。

            </p>
          </div>
        </div>
      </div>
    </div>
  );
};

export default GoogleSheetsTab;