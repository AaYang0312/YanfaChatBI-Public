import { getErrorMessage } from '../utils/message';
import React, { useEffect, useState, useRef } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { Loader2, AlertCircle, CheckCircle, Boxes } from 'lucide-react';
import { parseGoogleOAuthCallback } from '../utils/googleOAuth';
import authService from '../services/authService';
import { formatServiceError, productName, zhCN } from '../i18n/zh-CN';

const GoogleAuthCallback: React.FC = () => {
  const location = useLocation();
  const navigate = useNavigate();
  const [status, setStatus] = useState<'processing' | 'success' | 'error'>('processing');
  const [message, setMessage] = useState(zhCN.auth.google.processing);
  const hasProcessed = useRef(false); // Prevent double processing in StrictMode

  useEffect(() => {
    // Prevent double execution in React StrictMode
    if (hasProcessed.current) {
      return;
    }
    hasProcessed.current = true;

    const handleCallback = async () => {
      const params = new URLSearchParams(location.search);
      const error = params.get('error');
      const errorDescription = params.get('error_description');

      // Log for debugging
      console.log('Callback URL params:', {
        code: params.get('code'),
        state: params.get('state'),
        error,
        errorDescription
      });

      if (error) {
        const errorMsg = errorDescription || error;
        setStatus('error');
        setMessage(`${zhCN.auth.google.failedPrefix}${formatServiceError(errorMsg).summary}`);
        
        // Send error to parent window if opened as popup
        if (window.opener) {
          window.opener.postMessage({
            type: 'google-auth-error',
            error: errorMsg
          }, window.location.origin);
          
          setTimeout(() => {
            window.close();
          }, 2000);
        } else {
          setTimeout(() => {
            navigate('/auth');
          }, 3000);
        }
        return;
      }

      // Parse the OAuth callback
      const callbackData = parseGoogleOAuthCallback();
      
      if (!callbackData || !callbackData.code) {
        setStatus('error');
        setMessage(zhCN.auth.google.missingCode);
        
        if (window.opener) {
          window.opener.postMessage({
            type: 'google-auth-error',
            error: 'Invalid callback parameters'
          }, window.location.origin);
          
          setTimeout(() => {
            window.close();
          }, 2000);
        } else {
          setTimeout(() => {
            navigate('/auth');
          }, 3000);
        }
        return;
      }

      const { code, state } = callbackData;

      try {
        // Handle based on purpose
        if (state.purpose === 'auth') {
          // Authentication purpose - sign in to an existing account
          const redirectURI = import.meta.env.VITE_GOOGLE_REDIRECT_URI || 'http://localhost:5173/auth/google/callback';
          const response = await authService.googleOAuthCallback(code, redirectURI, state.action);
          
          setStatus('success');
          setMessage(zhCN.auth.google.success);
          
          // Store user info - response is already the AuthResponse
          const userData = response.user;
          if (userData) {
            console.log('Storing user info:', userData);
            localStorage.setItem('user', JSON.stringify(userData));
          }
          
          console.log('Auth successful, redirecting to dashboard...');
          // Redirect to dashboard
          setTimeout(() => {
            navigate('/');
          }, 1500);
          
        } else if (state.purpose === 'spreadsheet') {
          // Spreadsheet purpose - handled by GoogleSheetsTab
          // This callback is opened from GoogleSheetsTab which is already authenticated
          // Just pass the tokens back to the parent window via postMessage
          setStatus('success');
          setMessage(zhCN.auth.google.sheetsConnected);
          
          // Send tokens to parent window if opened as popup
          if (window.opener) {
            window.opener.postMessage({
              type: 'google-auth-success',
              code: code,
              access_token: code  // Will be exchanged by backend
            }, window.location.origin);
            
            setTimeout(() => {
              window.close();
            }, 1500);
          } else {
            // If not a popup, redirect back to the app
            setTimeout(() => {
              navigate('/');
            }, 1500);
          }
        } else {
          // Default to spreadsheet behavior for unknown purposes
          setStatus('success');
          setMessage(zhCN.auth.google.googleAuthSuccess);
          
          if (window.opener) {
            window.opener.postMessage({
              type: 'google-auth-success',
              code: code
            }, window.location.origin);
            
            setTimeout(() => {
              window.close();
            }, 1500);
          } else {
            setTimeout(() => {
              navigate('/');
            }, 1500);
          }
        }
        
      } catch (error: unknown) {
        const errorMsg = getErrorMessage(error, 'Authentication failed');
        setStatus('error');
        setMessage(formatServiceError(error).summary);
        console.error('OAuth callback error:', error);
        
        if (window.opener) {
          window.opener.postMessage({
            type: 'google-auth-error',
            error: errorMsg
          }, window.location.origin);
          
          setTimeout(() => {
            window.close();
          }, 2000);
        } else {
          setTimeout(() => {
            navigate('/auth');
          }, 3000);
        }
      }
    };

    handleCallback();
  }, [location, navigate]);

  return (
    <div className="min-h-screen flex items-center justify-center bg-gray-50">
      <div className="max-w-md w-full p-8">
        <h1 className="text-2xl md:text-3xl font-bold text-center mb-6 flex items-center justify-center gap-2">
          <Boxes className="w-10 h-10" />
          {productName}
        </h1>
        <div className="bg-white rounded-lg neo-border p-6">
          <div className="flex flex-col items-center space-y-4">
            {status === 'processing' && (
              <>
                <Loader2 className="w-12 h-12 animate-spin text-blue-600" />
                <h2 className="text-xl font-bold">{zhCN.auth.google.authenticating}</h2>
                <p className="text-gray-600 text-center">{message}</p>
              </>
            )}
            
            {status === 'success' && (
              <>
                <CheckCircle className="w-12 h-12 text-green-600" />
                <h2 className="text-xl font-bold text-green-800">{zhCN.auth.google.successTitle}</h2>
                <p className="text-gray-600 text-center">{message}</p>
                <p className="text-sm text-gray-500">{zhCN.auth.google.redirecting}</p>
              </>
            )}
            
            {status === 'error' && (
              <>
                <AlertCircle className="w-12 h-12 text-red-800" />
                <h2 className="text-xl font-bold text-red-800">{zhCN.auth.google.failedTitle}</h2>
                <p className="text-gray-600 text-center">{message}</p>
                <p className="text-sm text-gray-500">{zhCN.auth.google.redirectingLogin}</p>
              </>
            )}
          </div>
        </div>
      </div>
    </div>
  );
};

export default GoogleAuthCallback;