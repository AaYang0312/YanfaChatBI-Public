import React from 'react';
import { zhCN } from '../../../i18n/zh-CN';

interface SettingsTabProps {
  autoExecuteQuery: boolean;
  nonTechMode: boolean;
  autoGenerateVisualization: boolean;
  autoAnalyzeResults: boolean;
  setAutoExecuteQuery: (value: boolean) => void;
  setNonTechMode: (value: boolean) => void;
  setAutoGenerateVisualization: (value: boolean) => void;
  setAutoAnalyzeResults: (value: boolean) => void;
}

const SettingsTab: React.FC<SettingsTabProps> = ({
  autoExecuteQuery,
  nonTechMode,
  autoGenerateVisualization,
  autoAnalyzeResults,
  setAutoExecuteQuery,
  setNonTechMode,
  setAutoGenerateVisualization,
  setAutoAnalyzeResults,
}) => {
  return (
    <div className="space-y-6">
      <div className="neo-border p-4 rounded-lg">
        <div className="flex flex-col md:flex-row gap-4 md:gap-0 items-start md:items-center justify-between">
          <div>
            <label className="block font-bold mb-1 text-lg">{zhCN.connection.autoFetch}</label>
            <p className="text-gray-600 text-sm max-w-[480px]">{zhCN.connection.autoFetchHint}</p>
          </div>
          <label className="relative inline-flex items-center cursor-pointer">
            <input 
              type="checkbox" 
              className="sr-only peer" 
              checked={autoExecuteQuery}
              onChange={(e) => {
                const newValue = e.target.checked;
                setAutoExecuteQuery(newValue);
              }}
            />
            <div className="w-11 h-6 bg-gray-200 peer-focus:outline-none peer-focus:ring-4 peer-focus:ring-blue-300 rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-gray-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-blue-600"></div>
          </label>
        </div>
      </div>

      <div className="neo-border p-4 rounded-lg">
        <div className="flex flex-col md:flex-row gap-4 md:gap-0 items-start md:items-center justify-between">
          <div>
            <label className="block font-bold mb-1 text-lg">{zhCN.connection.nonTech}</label>
            <p className="text-gray-600 text-sm max-w-[480px]">{zhCN.connection.nonTechHint}</p>
          </div>
          <label className="relative inline-flex items-center cursor-pointer">
            <input 
              type="checkbox" 
              className="sr-only peer" 
              checked={nonTechMode}
              onChange={(e) => {
                setNonTechMode(e.target.checked);
              }}
            />
            <div className="w-11 h-6 bg-gray-200 peer-focus:outline-none peer-focus:ring-4 peer-focus:ring-blue-300 rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-gray-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-blue-600"></div>
          </label>
        </div>
      </div>

      <div className="neo-border p-4 rounded-lg">
        <div className="flex flex-col md:flex-row gap-4 md:gap-0 items-start md:items-center justify-between">
          <div>
            <label className="block font-bold mb-1 text-lg">{zhCN.connection.autoViz}</label>
            <p className="text-gray-600 text-sm max-w-[480px]">{zhCN.connection.autoVizHint}</p>
          </div>
          <label className="relative inline-flex items-center cursor-pointer">
            <input 
              type="checkbox" 
              className="sr-only peer" 
              checked={autoGenerateVisualization}
              onChange={(e) => {
                setAutoGenerateVisualization(e.target.checked);
              }}
            />
            <div className="w-11 h-6 bg-gray-200 peer-focus:outline-none peer-focus:ring-4 peer-focus:ring-blue-300 rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-gray-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-blue-600"></div>
          </label>
        </div>
      </div>

      <div className="neo-border p-4 rounded-lg">
        <div className="flex flex-col md:flex-row gap-4 md:gap-0 items-start md:items-center justify-between">
          <div>
            <label className="block font-bold mb-1 text-lg">{zhCN.connection.autoAnalyze}</label>
            <p className="text-gray-600 text-sm max-w-[480px]">{zhCN.connection.autoAnalyzeHint}</p>
          </div>
          <label className="relative inline-flex items-center cursor-pointer">
            <input 
              type="checkbox" 
              className="sr-only peer" 
              checked={autoAnalyzeResults}
              disabled={!autoExecuteQuery}
              onChange={(e) => {
                setAutoAnalyzeResults(e.target.checked);
              }}
            />
            <div className={`w-11 h-6 bg-gray-200 peer-focus:outline-none peer-focus:ring-4 peer-focus:ring-blue-300 rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-gray-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-blue-600 ${!autoExecuteQuery ? 'opacity-50' : ''}`}></div>
          </label>
        </div>
      </div>

      <div className="p-4 bg-yellow-100 border border-yellow-200 rounded-lg">
        <p className="text-sm text-yellow-700 font-medium">
          {zhCN.connection.moreSettings}
        </p>
      </div>
    </div>
  );
};

export default SettingsTab; 